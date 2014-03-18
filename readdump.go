package main

import (
	"bufio"
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"io"
	"log"
	"os"
	"runtime"
	"sort"
)

type fieldKind int
type typeKind int

const (
	fieldKindPtr    fieldKind = 0
	fieldKindString           = 1
	fieldKindSlice            = 2
	fieldKindIface            = 3
	fieldKindEface            = 4

	typeKindObject typeKind = 0
	typeKindArray           = 1
	typeKindChan            = 2

	tagObject     = 1
	tagEOF        = 3
	tagStackRoot  = 4
	tagDataRoot   = 5
	tagOtherRoot  = 6
	tagType       = 7
	tagGoRoutine  = 8
	tagStackFrame = 9
	tagParams     = 10
	tagFinalizer  = 11
	tagItab       = 12
	tagOSThread   = 13
	tagMemStats   = 14
)

type Dump struct {
	order      binary.ByteOrder
	ptrSize    uint64 // in bytes
	hChanSize  uint64 // channel header size in bytes
	heapStart  uint64
	heapEnd    uint64
	thechar    byte
	experiment string
	ncpu       uint64
	types      []*Type
	objects    []*Object
	frames     []*StackFrame
	goroutines []*GoRoutine
	stackroots []*StackRoot
	dataroots  []*DataRoot
	otherroots []*OtherRoot
	finalizers []*Finalizer
	itabs      []*Itab
	osthreads  []*OSThread
	memstats   *runtime.MemStats
}

// An edge is a directed connection between two objects.  The source
// object is implicit.  It includes information about where the edge
// leaves the source object and where it lands in the destination obj.
type Edge struct {
	to         *Object // object pointed to
	fromoffset uint64  // offset in source object where ptr was found
	tooffset   uint64  // offset in destination object where ptr lands
}

type Object struct {
	typ   *Type
	kind  typeKind
	data  []byte // length is sizeclass size, may be bigger then typ.size
	edges []Edge

	addr    uint64
	typaddr uint64
}

type StackRoot struct {
	frame *StackFrame
	e     Edge

	fromaddr  uint64
	toaddr    uint64
	frameaddr uint64
	depth     uint64
}
type DataRoot struct {
	name string // name of global variable
	e    Edge

	fromaddr uint64
	toaddr   uint64
}
type OtherRoot struct {
	description string
	e           Edge

	toaddr uint64
}

// Object obj has a finalizer.
type Finalizer struct {
	obj  uint64
	fn   uint64 // function to be run (a FuncVal*)
	code uint64 // code ptr (fn->fn)
	fint uint64 // type of function argument
	ot   uint64 // type of object
}

// For the given itab value, is the corresponding
// interface data field a pointer?
type Itab struct {
	addr uint64
	ptr  bool
}

type OSThread struct {
	addr   uint64
	id     uint64
	procid uint64
}

// A Field is a location in an object where there
// might be a pointer.
type Field struct {
	kind   fieldKind
	offset uint64
}

type Type struct {
	name     string // not necessarily unique
	size     uint64
	efaceptr bool // Efaces with this type have a data field which is a pointer
	fields   []Field

	addr uint64
}

type GoRoutine struct {
	tos  *StackFrame // frame at the top of the stack (i.e. currently running)
	ctxt *Object

	addr         uint64
	tosaddr      uint64
	goid         uint64
	gopc         uint64
	status       uint64
	issystem     bool
	isbackground bool
	waitsince    uint64
	waitreason   string
	ctxtaddr     uint64
	maddr        uint64
}

type StackFrame struct {
	name      string
	parent    *StackFrame
	goroutine *GoRoutine
	depth     uint64

	addr       uint64
	parentaddr uint64
	entry      uint64
	pc         uint64
}

func readUint64(r io.ByteReader) uint64 {
	x, err := binary.ReadUvarint(r)
	if err != nil {
		log.Fatal(err)
	}
	return x
}
func readNBytes(r io.ByteReader, n uint64) []byte {
	s := make([]byte, n)
	for i := range s {
		b, err := r.ReadByte()
		if err != nil {
			log.Fatal(err)
		}
		s[i] = b
	}
	return s
}
func readString(r io.ByteReader) string {
	n := readUint64(r)
	return string(readNBytes(r, n))
}
func readBool(r io.ByteReader) bool {
	b, err := r.ReadByte()
	if err != nil {
		log.Fatal(err)
	}
	return b != 0
}

// Reads heap dump into memory.
func rawRead(filename string) *Dump {
	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	r := bufio.NewReader(file)

	// check for header
	hdr, prefix, err := r.ReadLine()
	if err != nil {
		log.Fatal(err)
	}
	if prefix || string(hdr) != "go1.3 heap dump" {
		log.Fatal("not a go1.3 heap dump file")
	}

	var d Dump
	for {
		kind := readUint64(r)
		switch kind {
		case tagObject:
			obj := &Object{}
			obj.addr = readUint64(r)
			obj.typaddr = readUint64(r)
			obj.kind = typeKind(readUint64(r))
			size := readUint64(r)
			obj.data = readNBytes(r, size)
			d.objects = append(d.objects, obj)
		case tagEOF:
			return &d
		case tagStackRoot:
			t := &StackRoot{}
			t.fromaddr = readUint64(r)
			t.toaddr = readUint64(r)
			t.frameaddr = readUint64(r)
			t.depth = readUint64(r)
			d.stackroots = append(d.stackroots, t)
		case tagDataRoot:
			t := &DataRoot{}
			t.fromaddr = readUint64(r)
			t.toaddr = readUint64(r)
			d.dataroots = append(d.dataroots, t)
		case tagOtherRoot:
			t := &OtherRoot{}
			t.description = readString(r)
			t.toaddr = readUint64(r)
			d.otherroots = append(d.otherroots, t)
		case tagType:
			typ := &Type{}
			typ.addr = readUint64(r)
			typ.size = readUint64(r)
			typ.name = readString(r)
			typ.efaceptr = readBool(r)
			nptr := readUint64(r)
			typ.fields = make([]Field, nptr)
			for i := uint64(0); i < nptr; i++ {
				typ.fields[i].kind = fieldKind(readUint64(r))
				typ.fields[i].offset = readUint64(r)
			}
			d.types = append(d.types, typ)
		case tagGoRoutine:
			g := &GoRoutine{}
			g.addr = readUint64(r)
			g.tosaddr = readUint64(r)
			g.goid = readUint64(r)
			g.gopc = readUint64(r)
			g.status = readUint64(r)
			g.issystem = readBool(r)
			g.isbackground = readBool(r)
			g.waitsince = readUint64(r)
			g.waitreason = readString(r)
			g.ctxtaddr = readUint64(r)
			g.maddr = readUint64(r)
			d.goroutines = append(d.goroutines, g)
		case tagStackFrame:
			t := &StackFrame{}
			t.addr = readUint64(r)
			t.depth = readUint64(r)
			t.parentaddr = readUint64(r)
			t.entry = readUint64(r)
			t.pc = readUint64(r)
			t.name = readString(r)
			readString(r) // raw frame data
			d.frames = append(d.frames, t)
		case tagParams:
			if readUint64(r) == 0 {
				d.order = binary.LittleEndian
			} else {
				d.order = binary.BigEndian
			}
			d.ptrSize = readUint64(r)
			d.hChanSize = readUint64(r)
			d.heapStart = readUint64(r)
			d.heapEnd = readUint64(r)
			d.thechar = byte(readUint64(r))
			d.experiment = readString(r)
			d.ncpu = readUint64(r)
		case tagFinalizer:
			t := &Finalizer{}
			t.obj = readUint64(r)
			t.fn = readUint64(r)
			t.code = readUint64(r)
			t.fint = readUint64(r)
			t.ot = readUint64(r)
			d.finalizers = append(d.finalizers, t)
		case tagItab:
			t := &Itab{}
			t.addr = readUint64(r)
			t.ptr = readBool(r)
			d.itabs = append(d.itabs, t)
		case tagOSThread:
			t := &OSThread{}
			t.addr = readUint64(r)
			t.id = readUint64(r)
			t.procid = readUint64(r)
		case tagMemStats:
			t := &runtime.MemStats{}
			t.Alloc = readUint64(r)
			t.TotalAlloc = readUint64(r)
			t.Sys = readUint64(r)
			t.Lookups = readUint64(r)
			t.Mallocs = readUint64(r)
			t.Frees = readUint64(r)
			t.HeapAlloc = readUint64(r)
			t.HeapSys = readUint64(r)
			t.HeapIdle = readUint64(r)
			t.HeapInuse = readUint64(r)
			t.HeapReleased = readUint64(r)
			t.HeapObjects = readUint64(r)
			t.StackInuse = readUint64(r)
			t.StackSys = readUint64(r)
			t.MSpanInuse = readUint64(r)
			t.MSpanSys = readUint64(r)
			t.MCacheInuse = readUint64(r)
			t.MCacheSys = readUint64(r)
			t.BuckHashSys = readUint64(r)
			t.GCSys = readUint64(r)
			t.OtherSys = readUint64(r)
			t.NextGC = readUint64(r)
			t.LastGC = readUint64(r)
			t.PauseTotalNs = readUint64(r)
			for i := 0; i < 256; i++ {
				t.PauseNs[i] = readUint64(r)
			}
			t.NumGC = uint32(readUint64(r))
		default:
			log.Fatal("unknown record kind %d", kind)
		}
	}
}

type Heap []*Object

func (h Heap) Len() int           { return len(h) }
func (h Heap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h Heap) Less(i, j int) bool { return h[i].addr < h[j].addr }

// returns the object containing the pointed-to address, or nil if none.
func (h Heap) find(p uint64) *Object {
	j := sort.Search(len(h), func(i int) bool { return p < h[i].addr+uint64(len(h[i].data)) })
	if j < len(h) && p >= h[j].addr {
		return h[j]
	}
	return nil
}

type Global struct {
	name string
	addr uint64
}
type Globals []Global

func (g Globals) Len() int           { return len(g) }
func (g Globals) Swap(i, j int)      { g[i], g[j] = g[j], g[i] }
func (g Globals) Less(i, j int) bool { return g[i].addr < g[j].addr }

// returns the global variable containing the given address.
func (g Globals) find(p uint64) Global {
	j := sort.Search(len(g), func(i int) bool { return p < g[i].addr })
	if j == 0 {
		return Global{"unknown global", 0}
	}
	return g[j-1]
}

func getDwarf(execname string) *dwarf.Data {
	e, err := elf.Open(execname)
	if err == nil {
		defer e.Close()
		d, err := e.DWARF()
		if err == nil {
			return d
		}
	}
	m, err := macho.Open(execname)
	if err == nil {
		defer m.Close()
		d, err := m.DWARF()
		if err == nil {
			return d
		}
	}
	p, err := pe.Open(execname)
	if err == nil {
		defer p.Close()
		d, err := p.DWARF()
		if err == nil {
			return d
		}
	}
	log.Fatal("can't get dwarf info from executable", err)
	return nil
}

func globalMap(d *Dump, execname string) Globals {
	var g Globals
	w := getDwarf(execname)
	r := w.Reader()
	for {
		e, err := r.Next()
		if err != nil {
			log.Fatal(err)
		}
		if e == nil {
			break
		}
		if e.Tag != dwarf.TagVariable {
			continue
		}
		name := e.Val(dwarf.AttrName).(string)
		locexpr := e.Val(dwarf.AttrLocation).([]uint8)
		if len(locexpr) > 0 && locexpr[0] == 0x03 { // DW_OP_addr
			loc := readPtr(d, locexpr[1:])
			g = append(g, Global{name, loc})
		}
	}
	sort.Sort(g)
	return g
}

func linkPtr(info *LinkInfo, x *Object, off uint64) {
	p := readPtr(info.dump, x.data[off:])
	q := info.heap.find(p)
	if q != nil {
		x.edges = append(x.edges, Edge{q, off, p - q.addr})
	}
}
func linkFields(info *LinkInfo, x *Object, fields []Field, offset uint64) {
	for _, f := range fields {
		off := offset + f.offset
		switch f.kind {
		case fieldKindPtr:
			linkPtr(info, x, off)
		case fieldKindString:
			linkPtr(info, x, off)
		case fieldKindSlice:
			linkPtr(info, x, off)
		case fieldKindEface:
			linkPtr(info, x, off)
			tp := readPtr(info.dump, x.data[off:])
			if tp != 0 {
				t := info.types[tp]
				if t == nil {
					log.Fatal("can't find eface type")
				}
				if t.efaceptr {
					linkPtr(info, x, off+info.dump.ptrSize)
				}
			}
		case fieldKindIface:
			tp := readPtr(info.dump, x.data[off:])
			if tp != 0 {
				t := info.itabs[tp]
				if t == nil {
					log.Fatal("can't find iface tab")
				}
				if t.ptr {
					linkPtr(info, x, off+info.dump.ptrSize)
				}
			}
		}
	}
}

// various maps used to link up data structures
type LinkInfo struct {
	dump    *Dump
	types   map[uint64]*Type
	itabs   map[uint64]*Itab
	frames  map[frameId]*StackFrame
	globals Globals
	heap    Heap
}

type frameId struct {
	sp    uint64
	depth uint64
}

func link(d *Dump, execname string) {
	// initialize some maps used for linking
	var info LinkInfo
	info.dump = d
	info.types = make(map[uint64]*Type, len(d.types))
	info.itabs = make(map[uint64]*Itab, len(d.itabs))
	info.frames = make(map[frameId]*StackFrame, len(d.frames))
	for _, x := range d.types {
		// Note: there may be duplicate type records in a dump.
		// The duplicates get thrown away here.
		info.types[x.addr] = x
	}
	for _, x := range d.itabs {
		info.itabs[x.addr] = x
	}
	for _, x := range d.frames {
		info.frames[frameId{x.addr, x.depth}] = x
	}

	// Binary-searchable map of global variables
	info.globals = globalMap(d, execname)

	// Binary-searchable map of objects
	for _, x := range d.objects {
		info.heap = append(info.heap, x)
	}
	sort.Sort(info.heap)

	// link objects to types
	for _, x := range d.objects {
		if x.typaddr == 0 {
			x.typ = nil
		} else {
			x.typ = info.types[x.typaddr]
			if x.typ == nil {
				log.Fatal("type is missing")
			}
		}
	}

	// link up frames in sequence
	for _, f := range d.frames {
		f.parent = info.frames[frameId{f.parentaddr, f.depth + 1}]
		// NOTE: the base frame of the stack (runtime.goexit usually)
		// will fail the lookup here and set a nil pointer.
	}

	// link goroutines to frames & vice versa
	for _, g := range d.goroutines {
		g.tos = info.frames[frameId{g.tosaddr, 0}]
		if g.tos == nil {
			log.Fatal("tos missing")
		}
		for f := g.tos; f != nil; f = f.parent {
			f.goroutine = g
		}
		x := info.heap.find(g.ctxtaddr)
		if x != nil {
			g.ctxt = x
		}
	}

	// link up roots to objects
	for _, r := range d.stackroots {
		r.frame = info.frames[frameId{r.frameaddr, r.depth}]
		x := info.heap.find(r.toaddr)
		if x != nil {
			r.e = Edge{x, r.fromaddr - r.frameaddr, r.toaddr - x.addr}
		}
	}
	for _, r := range d.dataroots {
		g := info.globals.find(r.fromaddr)
		r.name = g.name
		q := info.heap.find(r.toaddr)
		if q != nil {
			r.e = Edge{q, r.fromaddr - g.addr, r.toaddr - q.addr}
		}
	}
	for _, r := range d.otherroots {
		x := info.heap.find(r.toaddr)
		if x != nil {
			r.e = Edge{x, 0, r.toaddr - x.addr}
		}
	}

	// link objects to each other
	for _, x := range d.objects {
		t := x.typ
		if t == nil {
			continue // typeless objects have no pointers
		}
		switch x.kind {
		case typeKindObject:
			linkFields(&info, x, t.fields, 0)
		case typeKindArray:
			for i := uint64(0); i <= uint64(len(x.data))-t.size; i += t.size {
				linkFields(&info, x, t.fields, i)
			}
		case typeKindChan:
			for i := d.hChanSize; i <= uint64(len(x.data))-t.size; i += t.size {
				linkFields(&info, x, t.fields, i)
			}
		}
	}

	// Add links for finalizers
	for _, f := range d.finalizers {
		x := info.heap.find(f.obj)
		for _, addr := range []uint64{f.fn, f.fint, f.ot} {
			y := info.heap.find(addr)
			if x != nil && y != nil {
				x.edges = append(x.edges, Edge{x, 0, addr - y.addr})
				// TODO: mark edge as arising from a finalizer somehow?
			}
		}
	}
}

func Read(dumpname, execname string) *Dump {
	d := rawRead(dumpname)
	link(d, execname)
	return d
}

func readPtr(d *Dump, b []byte) uint64 {
	switch {
	case d.order == binary.LittleEndian && d.ptrSize == 4:
		return uint64(b[0]) + uint64(b[1])<<8 + uint64(b[2])<<16 + uint64(b[3])<<24
	case d.order == binary.BigEndian && d.ptrSize == 4:
		return uint64(b[3]) + uint64(b[2])<<8 + uint64(b[1])<<16 + uint64(b[0])<<24
	case d.order == binary.LittleEndian && d.ptrSize == 8:
		return uint64(b[0]) + uint64(b[1])<<8 + uint64(b[2])<<16 + uint64(b[3])<<24 + uint64(b[4])<<32 + uint64(b[5])<<40 + uint64(b[6])<<48 + uint64(b[7])<<56
	case d.order == binary.BigEndian && d.ptrSize == 8:
		return uint64(b[7]) + uint64(b[6])<<8 + uint64(b[5])<<16 + uint64(b[4])<<24 + uint64(b[3])<<32 + uint64(b[2])<<40 + uint64(b[1])<<48 + uint64(b[0])<<56
	default:
		log.Fatal("unsupported order=%v ptrSize=%d", d.order, d.ptrSize)
		return 0
	}
}
