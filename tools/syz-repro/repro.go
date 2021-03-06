// Copyright 2015 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/syzkaller/config"
	"github.com/google/syzkaller/csource"
	"github.com/google/syzkaller/fileutil"
	"github.com/google/syzkaller/prog"
	"github.com/google/syzkaller/vm"
	_ "github.com/google/syzkaller/vm/kvm"
	_ "github.com/google/syzkaller/vm/qemu"
)

var (
	flagConfig = flag.String("config", "", "configuration file")
	flagCount  = flag.Int("count", 0, "number of VMs to use (overrides config count param)")

	instances    chan vm.Instance
	bootRequests chan bool
)

func main() {
	flag.Parse()
	cfg, _, _, err := config.Parse(*flagConfig)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if *flagCount > 0 {
		cfg.Count = *flagCount
	}

	if len(flag.Args()) != 1 {
		log.Fatalf("usage: syz-repro -config=config.file execution.log")
	}
	data, err := ioutil.ReadFile(flag.Args()[0])
	if err != nil {
		log.Fatalf("failed to open log file: %v", err)
	}
	entries := prog.ParseLog(data)
	log.Printf("parsed %v programs", len(entries))

	crashLoc := vm.CrashRe.FindIndex(data)
	if crashLoc == nil {
		log.Fatalf("can't find crash message in the log")
	}
	log.Printf("target crash: '%s'", data[crashLoc[0]:crashLoc[1]])

	instances = make(chan vm.Instance, cfg.Count)
	bootRequests = make(chan bool, cfg.Count)
	for i := 0; i < cfg.Count; i++ {
		bootRequests <- true
		go func() {
			for range bootRequests {
				vmCfg, err := config.CreateVMConfig(cfg)
				if err != nil {
					log.Fatalf("failed to create VM config: %v", err)
				}
				inst, err := vm.Create(cfg.Type, vmCfg)
				if err != nil {
					log.Fatalf("failed to create VM: %v", err)
				}
				if err := inst.Copy(filepath.Join(cfg.Syzkaller, "bin/syz-execprog"), "/syz-execprog"); err != nil {
					log.Fatalf("failed to copy to VM: %v", err)
				}
				if err := inst.Copy(filepath.Join(cfg.Syzkaller, "bin/syz-executor"), "/syz-executor"); err != nil {
					log.Fatalf("failed to copy to VM: %v", err)
				}
				instances <- inst
			}
		}()
	}

	repro(cfg, entries, crashLoc)

	for {
		select {
		case inst := <-instances:
			inst.Close()
		default:
			return
		}
	}
}

func repro(cfg *config.Config, entries []*prog.LogEntry, crashLoc []int) {
	// Cut programs that were executed after crash.
	for i, ent := range entries {
		if ent.Start > crashLoc[0] {
			entries = entries[:i]
			break
		}
	}
	// Extract last program on every proc.
	procs := make(map[int]int)
	for i, ent := range entries {
		procs[ent.Proc] = i
	}
	var indices []int
	for _, idx := range procs {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	var suspected []*prog.LogEntry
	for i := len(indices) - 1; i >= 0; i-- {
		suspected = append(suspected, entries[indices[i]])
	}
	// Execute the suspected programs.
	log.Printf("the suspected programs are:")
	for _, ent := range suspected {
		log.Printf("on proc %v:\n%s\n", ent.Proc, ent.P.Serialize())
	}
	var p *prog.Prog
	multiplier := 1
	for ; p == nil && multiplier <= 100; multiplier *= 10 {
		for _, ent := range suspected {
			if testProg(cfg, ent.P, multiplier, true, true) {
				p = ent.P
				break
			}
		}
	}
	if p == nil {
		log.Printf("no program crashed")
		return
	}
	log.Printf("minimizing program")

	p, _ = prog.Minimize(p, -1, func(p1 *prog.Prog, callIndex int) bool {
		return testProg(cfg, p1, multiplier, true, true)
	})

	opts := csource.Options{
		Threaded: true,
		Collide:  true,
	}
	if testProg(cfg, p, multiplier, true, false) {
		opts.Collide = false
		if testProg(cfg, p, multiplier, false, false) {
			opts.Threaded = false
		}
	}

	src := csource.Write(p, opts)
	log.Printf("C source:\n%s\n", src)
	srcf, err := fileutil.WriteTempFile(src)
	if err != nil {
		log.Fatalf("%v", err)
	}
	bin, err := csource.Build(srcf)
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer os.Remove(bin)
	testBin(cfg, bin)
}

func returnInstance(inst vm.Instance, res bool) {
	if res {
		// The test crashed, discard the VM and issue another boot request.
		bootRequests <- true
		inst.Close()
	} else {
		// The test did not crash, reuse the same VM in future.
		instances <- inst
	}
}

func testProg(cfg *config.Config, p *prog.Prog, multiplier int, threaded, collide bool) (res bool) {
	log.Printf("booting VM")
	inst := <-instances
	defer func() {
		returnInstance(inst, res)
	}()

	pstr := p.Serialize()
	progFile, err := fileutil.WriteTempFile(pstr)
	if err != nil {
		log.Fatalf("%v", err)
	}
	defer os.Remove(progFile)
	if err := inst.Copy(progFile, "/syz-prog"); err != nil {
		log.Fatalf("failed to copy to VM: %v", err)
	}

	repeat := 100
	timeoutSec := 10 * repeat / cfg.Procs
	if threaded {
		repeat *= 10
		timeoutSec *= 1
	}
	repeat *= multiplier
	timeoutSec *= multiplier
	timeout := time.Duration(timeoutSec) * time.Second
	command := fmt.Sprintf("/syz-execprog -executor /syz-executor -cover=0 -procs=%v -repeat=%v -threaded=%v -collide=%v /syz-prog",
		cfg.Procs, repeat, threaded, collide)
	log.Printf("testing program (threaded=%v, collide=%v, repeat=%v, timeout=%v):\n%s\n",
		threaded, collide, repeat, timeout, pstr)
	return testImpl(inst, command, timeout)
}

func testBin(cfg *config.Config, bin string) (res bool) {
	log.Printf("booting VM")
	inst := <-instances
	defer func() {
		returnInstance(inst, res)
	}()

	if err := inst.Copy(bin, "/syz-bin"); err != nil {
		log.Fatalf("failed to copy to VM: %v", err)
	}
	log.Printf("testing compiled C program")
	return testImpl(inst, "/syz-bin", 10*time.Second)
}

func testImpl(inst vm.Instance, command string, timeout time.Duration) (res bool) {
	outc, errc, err := inst.Run(timeout, command)
	if err != nil {
		log.Fatalf("failed to run command in VM: %v", err)
	}
	var output []byte
	for {
		select {
		case out := <-outc:
			output = append(output, out...)
			if loc := vm.CrashRe.FindIndex(output); loc != nil {
				log.Printf("program crashed with '%s'", output[loc[0]:loc[1]])
				return true
			}
		case err := <-errc:
			if err != nil {
				log.Printf("program crashed with result '%v'", err)
				return true
			}
			log.Printf("program did not crash")
			return false
		}
	}
}
