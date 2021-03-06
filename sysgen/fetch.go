// Copyright 2015 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// fetchValues converts literal constants (e.g. O_APPEND) or any other C expressions
// into their respective numeric values. It does so by builting and executing a C program
// that prints values of the provided expressions.
func fetchValues(vals []string, includes []string, defines map[string]string) []string {
	includeText := ""
	for _, inc := range includes {
		includeText += fmt.Sprintf("#include <%v>\n", inc)
	}
	definesText := ""
	for k, v := range defines {
		definesText += fmt.Sprintf("#ifndef %v\n#define %v %v\n#endif\n", k, k, v)
	}
	src := strings.Replace(fetchSrc, "[[INCLUDES]]", includeText, 1)
	src = strings.Replace(src, "[[DEFAULTS]]", definesText, 1)
	src = strings.Replace(src, "[[VALS]]", strings.Join(vals, ","), 1)
	bin, err := ioutil.TempFile("", "")
	if err != nil {
		failf("failed to create temp file: %v", err)
	}
	bin.Close()
	defer os.Remove(bin.Name())

	cmd := exec.Command("gcc", "-x", "c", "-", "-o", bin.Name())
	cmd.Stdin = strings.NewReader(src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		failf("failed to run gcc: %v\n%v", err, string(out))
	}

	out, err = exec.Command(bin.Name()).CombinedOutput()
	if err != nil {
		failf("failed to flags binary: %v\n%v", err, string(out))
	}

	flagVals := strings.Split(string(out), " ")
	if len(flagVals) != len(vals) {
		failf("fetched wrong number of values")
	}
	for _, v := range flagVals {
		_, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			failf("failed to parse value: %v (%v)", err, v)
		}
	}
	return flagVals
}

var fetchSrc = `
#define _GNU_SOURCE
#include <stdio.h>
[[INCLUDES]]

[[DEFAULTS]]

int main() {
	int i;
	unsigned long vals[] = {[[VALS]]};
	for (i = 0; i < sizeof(vals)/sizeof(vals[0]); i++) {
		if (i != 0)
			printf(" ");
		printf("%lu", vals[i]);
	}
	return 0;
}
`
