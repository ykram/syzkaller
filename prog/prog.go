// Copyright 2015 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package prog

import (
	"github.com/google/syzkaller/sys"
)

type Prog struct {
	Calls []*Call
}

type Call struct {
	Meta *sys.Call
	Args []*Arg
	Ret  *Arg
}

type Arg struct {
	Call       *Call
	Type       sys.Type
	Kind       ArgKind
	Dir        ArgDir
	Val        uintptr       // value of ArgConst
	AddrPage   uintptr       // page index for ArgPointer address, page count for ArgPageSize
	AddrOffset int           // page offset for ArgPointer address
	Data       []byte        // data of ArgData
	Inner      []*Arg        // subargs of ArgGroup
	Res        *Arg          // target of ArgResult, pointee for ArgPointer
	Uses       map[*Arg]bool // this arg is used by those ArgResult args
	OpDiv      uintptr       // divide result for ArgResult (executed before OpAdd)
	OpAdd      uintptr       // add to result for ArgResult

	// ArgUnion/UnionType
	Option     *Arg
	OptionType sys.Type
}

type ArgKind int

const (
	ArgConst ArgKind = iota
	ArgResult
	ArgPointer  // even if these are always constant (for reproducibility), we use a separate type because they are represented in an abstract (base+page+offset) form
	ArgPageSize // same as ArgPointer but base is not added, so it represents "lengths" in pages
	ArgData
	ArgGroup // logical group of args (struct or array)
	ArgUnion
	ArgReturn // fake value denoting syscall return value
)

type ArgDir sys.Dir

const (
	DirIn    = ArgDir(sys.DirIn)
	DirOut   = ArgDir(sys.DirOut)
	DirInOut = ArgDir(sys.DirInOut)
)

func (a *Arg) Size(typ sys.Type) uintptr {
	switch typ1 := typ.(type) {
	case sys.IntType, sys.LenType, sys.FlagsType, sys.ConstType, sys.StrConstType,
		sys.FileoffType, sys.ResourceType, sys.VmaType, sys.PtrType:
		return typ.Size()
	case sys.FilenameType:
		return uintptr(len(a.Data))
	case sys.BufferType:
		return uintptr(len(a.Data))
	case sys.StructType:
		var size uintptr
		for i, f := range typ1.Fields {
			size += a.Inner[i].Size(f)
		}
		return size
	case sys.UnionType:
		return a.Option.Size(a.OptionType)
	case sys.ArrayType:
		var size uintptr
		for _, in := range a.Inner {
			size += in.Size(typ1.Type)
		}
		return size
	default:
		panic("unknown arg type")
	}
}

func constArg(v uintptr) *Arg {
	return &Arg{Kind: ArgConst, Val: v}
}

func resultArg(r *Arg) *Arg {
	arg := &Arg{Kind: ArgResult, Res: r}
	if r.Uses == nil {
		r.Uses = make(map[*Arg]bool)
	}
	if r.Uses[arg] {
		panic("already used")
	}
	r.Uses[arg] = true
	return arg
}

func dataArg(data []byte) *Arg {
	return &Arg{Kind: ArgData, Data: append([]byte{}, data...)}
}

func pointerArg(page uintptr, off int, obj *Arg) *Arg {
	return &Arg{Kind: ArgPointer, AddrPage: page, AddrOffset: off, Res: obj}
}

func pageSizeArg(npages uintptr, off int) *Arg {
	return &Arg{Kind: ArgPageSize, AddrPage: npages, AddrOffset: off}
}

func groupArg(inner []*Arg) *Arg {
	return &Arg{Kind: ArgGroup, Inner: inner}
}

func unionArg(opt *Arg, typ sys.Type) *Arg {
	return &Arg{Kind: ArgUnion, Option: opt, OptionType: typ}
}

func returnArg() *Arg {
	return &Arg{Kind: ArgReturn, Dir: DirOut}
}

func (p *Prog) insertBefore(c *Call, calls []*Call) {
	idx := 0
	for ; idx < len(p.Calls); idx++ {
		if p.Calls[idx] == c {
			break
		}
	}
	var newCalls []*Call
	newCalls = append(newCalls, p.Calls[:idx]...)
	newCalls = append(newCalls, calls...)
	if idx < len(p.Calls) {
		newCalls = append(newCalls, p.Calls[idx])
		newCalls = append(newCalls, p.Calls[idx+1:]...)
	}
	p.Calls = newCalls
}
