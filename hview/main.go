package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"github.com/randall77/hprof/read"
	"html"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"text/template"
)

const (
	defaultAddr = ":8080" // default webserver address
)

var (
	httpAddr = flag.String("http", defaultAddr, "HTTP service address")
)

// d is the loaded heap dump.
var d *read.Dump

// link to type's page
func typeLink(ft *read.FullType) string {
	return fmt.Sprintf("<a href=\"type?id=%d\">%s</a>", ft.Id, ft.Name)
}

func objLink(x read.ObjId) string {
	return fmt.Sprintf("<a href=obj?id=%d>object %x</a>", x, d.Addr(x))
}

// returns an html string representing the target of an Edge
func edgeLink(e read.Edge) string {
	s := objLink(e.To)
	if e.ToOffset != 0 {
		s = fmt.Sprintf("%s+%d", s, e.ToOffset)
	}
	return s
}

// returns an html string representing the source of an Edge
func edgeSource(x read.ObjId, e read.Edge) string {
	s := objLink(x)
	if e.FieldName != "" {
		s = fmt.Sprintf("%s.%s", s, e.FieldName)
	}
	if e.ToOffset != 0 {
		s = fmt.Sprintf("%s+%d", s, e.ToOffset)
	}
	return s
}

// the first d.PtrSize bytes of b contain a pointer.  Return html
// to represent that pointer.
func nonheapPtr(b []byte) string {
	p := readPtr(b)
	if p == 0 {
		return "nil"
	} else {
		// TODO: look up symbol in executable
		return fmt.Sprintf("outsideheap_%x", p)
	}
}

// display field
type Field struct {
	Name  string
	Typ   string
	Value string
}

// rawBytes generates an html string representing the given raw bytes
func rawBytes(b []byte) string {
	v := ""
	s := ""
	for _, c := range b {
		v += fmt.Sprintf("%.2x ", c)
		if c <= 32 || c >= 127 {
			c = 46
		}
		s += fmt.Sprintf("%c", c)
	}
	return v + " | " + html.EscapeString(s)
}

// getFields uses the data in b to fill in the values for the given field list.
// edges is a list of known connecting out edges.
func getFields(b []byte, fields []read.Field, edges []read.Edge) []Field {
	var r []Field
	off := uint64(0)
	for _, f := range fields {
		if f.Offset < off {
			log.Fatal("out of order fields")
		}
		if f.Offset > off {
			r = append(r, Field{fmt.Sprintf("<font color=LightGray>pad %d</font>", f.Offset-off), "", ""})
			off = f.Offset
		}
		var value string
		var typ string
		switch f.Kind {
		case read.FieldKindBool:
			if b[off] == 0 {
				value = "false"
			} else {
				value = "true"
			}
			typ = "bool"
			off++
		case read.FieldKindUInt8:
			value = fmt.Sprintf("%d", b[off])
			typ = "uint8"
			off++
		case read.FieldKindSInt8:
			value = fmt.Sprintf("%d", int8(b[off]))
			typ = "int8"
			off++
		case read.FieldKindUInt16:
			value = fmt.Sprintf("%d", read16(b[off:]))
			typ = "uint16"
			off += 2
		case read.FieldKindSInt16:
			value = fmt.Sprintf("%d", int16(read16(b[off:])))
			typ = "int16"
			off += 2
		case read.FieldKindUInt32:
			value = fmt.Sprintf("%d", read32(b[off:]))
			typ = "uint32"
			off += 4
		case read.FieldKindSInt32:
			value = fmt.Sprintf("%d", int32(read32(b[off:])))
			typ = "int32"
			off += 4
		case read.FieldKindUInt64:
			value = fmt.Sprintf("%d", read64(b[off:]))
			typ = "uint64"
			off += 8
		case read.FieldKindSInt64:
			value = fmt.Sprintf("%d", int64(read64(b[off:])))
			typ = "int64"
			off += 8
		case read.FieldKindBytes8:
			value = rawBytes(b[off:off+8])
			typ = "raw bytes"
			off += 8
		case read.FieldKindBytes16:
			value = rawBytes(b[off:off+16])
			typ = "raw bytes"
			off += 16
		case read.FieldKindPtr:
			typ = "ptr"
			// TODO: get ptr base type somehow?  Also for slices,chans.
			if len(edges) > 0 && edges[0].FromOffset == off {
				value = edgeLink(edges[0])
				edges = edges[1:]
			} else {
				value = nonheapPtr(b[off:])
			}
			off += d.PtrSize
		case read.FieldKindIface:
			// TODO: the itab part?
			typ = "interface{...}"
			if len(edges) > 0 && edges[0].FromOffset == off+d.PtrSize {
				value = edgeLink(edges[0])
				edges = edges[1:]
			} else {
				// TODO: use itab to decide whether this is a
				// pointer or a scalar.
				value = nonheapPtr(b[off+d.PtrSize:])
			}
			off += 2 * d.PtrSize
		case read.FieldKindEface:
			// TODO: the type part
			typ = "interface{}"
			if len(edges) > 0 && edges[0].FromOffset == off+d.PtrSize {
				value = edgeLink(edges[0])
				edges = edges[1:]
			} else {
				// TODO: use type to decide whether this is a
				// pointer or a scalar.
				value = nonheapPtr(b[off+d.PtrSize:])
			}
			off += 2 * d.PtrSize
		case read.FieldKindString:
			typ = "string"
			if len(edges) > 0 && edges[0].FromOffset == off {
				value = edgeLink(edges[0])
				edges = edges[1:]
			} else {
				value = nonheapPtr(b[off:])
			}
			value = fmt.Sprintf("%s/%d", value, readPtr(b[off+d.PtrSize:]))
			off += 2 * d.PtrSize
		case read.FieldKindSlice:
			typ = "slice"
			if len(edges) > 0 && edges[0].FromOffset == off {
				value = edgeLink(edges[0])
				edges = edges[1:]
			} else {
				value = nonheapPtr(b[off:])
			}
			value = fmt.Sprintf("%s/%d/%d", value, readPtr(b[off+d.PtrSize:]), readPtr(b[off+2*d.PtrSize:]))
			off += 3 * d.PtrSize
		}
		r = append(r, Field{f.Name, typ, value})
	}
	if uint64(len(b)) > off {
		r = append(r, Field{fmt.Sprintf("<font color=LightGray>sizeclass pad %d</font>", uint64(len(b))-off), "", ""})
	}
	return r
}

type objInfo struct {
	Addr         uint64
	Typ          string
	Size         uint64
	Fields       []Field
	Referrers    []string
	ReachableMem uint64
	Roots        []string
}

var objTemplate = template.Must(template.New("obj").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Object {{printf "%x" .Addr}}</title>
</head>
<body>
<tt>
<h2>Object {{printf "%x" .Addr}} : {{.Typ}}</h2>
<h3>{{.Size}} bytes</h3>
<table>
<tr>
<td>Field</td>
<td>Type</td>
<td>Value</td>
</tr>
{{range .Fields}}
<tr>
<td>{{.Name}}</td>
<td>{{.Typ}}</td>
<td>{{.Value}}</td>
</tr>
{{end}}
</table>
<h3>Referrers</h3>
{{range .Referrers}}
{{.}}
<br>
{{end}}
<h3>Reachable Memory</h3>
{{.ReachableMem}} bytes
</tt>
</body>
</html>
`))

func objHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := q["id"]
	if len(v) != 1 {
		http.Error(w, "id parameter missing", 405)
		return
	}
	id, err := strconv.ParseUint(v[0], 10, 64)
	if err != nil {
		http.Error(w, err.Error(), 405)
		return
	}

	if int(id) >= len(d.Objects) {
		http.Error(w, "object not found", 405)
		return
	}
	x := read.ObjId(id)

	// compute amount of reachable memory.
	// TODO: do as a preprocess?
	reachableMem := uint64(0)
	h := map[read.ObjId]struct{}{}
	var queue []read.ObjId
	h[x] = struct{}{}
	queue = append(queue, x)
	for len(queue) > 0 {
		y := queue[0]
		queue = queue[1:]
		reachableMem += d.Size(y)
		for _, e := range d.Edges(y) {
			if _, ok := h[e.To]; !ok {
				h[e.To] = struct{}{}
				queue = append(queue, e.To)
			}
		}
	}

	info := objInfo {
		d.Addr(x),
		typeLink(d.Ft(x)),
		d.Size(x),
		getFields(d.Contents(x), d.Ft(x).Fields, d.Edges(x)),
		getReferrers(x),
		reachableMem,
		nil,
	}
	if err := objTemplate.Execute(w, info); err != nil {
		log.Print(err)
	}
}

type objEntry struct {
	Id read.ObjId
	Addr uint64
}
type typeInfo struct {
	Name      string
	Size      uint64
	Instances []string
}

var typeTemplate = template.Must(template.New("type").Parse(`
<html>
<head>
<title>Type {{.Name}}</title>
</head>
<body>
<tt>
<h2>{{.Name}}</h2>
<h3>Size {{.Size}}</h3>
<h3>Instances</h3>
<table>
{{range .Instances}}
<tr><td>{{.}}</td></tr>
{{end}}
</table>
</tt>
</body>
</html>
`))

func typeHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s := q["id"]
	if len(s) != 1 {
		http.Error(w, "type id missing", 405)
		return
	}
	id, err := strconv.ParseUint(s[0], 10, 64)
	if err != nil {
		http.Error(w, err.Error(), 405)
		return
	}

	if id >= uint64(len(d.FTList)) {
		http.Error(w, "can't find type", 405)
		return
	}

	ft := d.FTList[id]
	var info typeInfo
	info.Name = ft.Name
	info.Size = ft.Size
	for _, x := range byType[ft.Id].objects {
		info.Instances = append(info.Instances, objLink(x))
	}
	if err := typeTemplate.Execute(w, info); err != nil {
		log.Print(err)
	}
}

type hentry struct {
	Name  string
	Count int
	Bytes uint64
}

var histoTemplate = template.Must(template.New("histo").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Type histogram</title>
</head>
<body>
<tt>
<table>
<col align="left">
<col align="right">
<col align="right">
<tr>
<td>Type</td>
<td align="right">Count</td>
<td align="right">Bytes</td>
</tr>
{{range .}}
<tr>
<td>{{.Name}}</td>
<td align="right">{{.Count}}</td>
<td align="right">{{.Bytes}}</td>
</tr>
{{end}}
</table>
</tt>
</body>
</html>
`))

func histoHandler(w http.ResponseWriter, r *http.Request) {
	// build sorted list of types
	var s []hentry
	for id, b := range byType {
		ft := d.FTList[id]
		s = append(s, hentry{typeLink(ft), len(b.objects), b.bytes})
	}
	sort.Sort(ByBytes(s))

	if err := histoTemplate.Execute(w, s); err != nil {
		log.Print(err)
	}
}

type ByBytes []hentry

func (a ByBytes) Len() int           { return len(a) }
func (a ByBytes) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByBytes) Less(i, j int) bool { return a[i].Bytes > a[j].Bytes }

var mainTemplate = template.Must(template.New("histo").Parse(`
<html>
<head>
<title>Heap dump viewer</title>
</head>
<body>
<tt>

<h2>Heap dump viewer</h2>
<br>
Heap size: {{.Memstats.Alloc}} bytes
<br>
Heap objects: {{len .Objects}}
<br>
<a href="histo">Type Histogram</a>
<a href="globals">Globals</a>
<a href="goroutines">Goroutines</a>
<a href="others">Miscellaneous Roots</a>
</tt>
</body>
</html>
`))

func mainHandler(w http.ResponseWriter, r *http.Request) {
	if err := mainTemplate.Execute(w, d); err != nil {
		log.Print(err)
	}
}

var globalsTemplate = template.Must(template.New("globals").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Global roots</title>
</head>
<body>
<tt>
<h2>Global roots</h2>
<table>
<tr>
<td>Name</td>
<td>Type</td>
<td>Value</td>
</tr>
{{range .}}
<tr>
<td>{{.Name}}</td>
<td>{{.Typ}}</td>
<td>{{.Value}}</td>
</tr>
{{end}}
</table>
</tt>
</body>
</html>
`))

func globalsHandler(w http.ResponseWriter, r *http.Request) {
	var f []Field
	for _, x := range []*read.Data{d.Data, d.Bss} {
		f = append(f, getFields(x.Data, x.Fields, x.Edges)...)
	}
	if err := globalsTemplate.Execute(w, f); err != nil {
		log.Print(err)
	}
}

var othersTemplate = template.Must(template.New("others").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Other roots</title>
</head>
<body>
<tt>
<h2>Other roots</h2>
<table>
<tr>
<td>Name</td>
<td>Type</td>
<td>Value</td>
</tr>
{{range .}}
<tr>
<td>{{.Name}}</td>
<td>{{.Typ}}</td>
<td>{{.Value}}</td>
</tr>
{{end}}
</table>
</tt>
</body>
</html>
`))

func othersHandler(w http.ResponseWriter, r *http.Request) {
	var f []Field
	for _, x := range d.Otherroots {
		f = append(f, Field{x.Description, "unknown", edgeLink(x.E)})
	}
	if err := othersTemplate.Execute(w, f); err != nil {
		log.Print(err)
	}
}

type goListInfo struct {
	Name  string
	State string
}

var goListTemplate = template.Must(template.New("golist").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Goroutines</title>
</head>
<body>
<tt>
<h2>Goroutines</h2>
<table>
<tr>
<td>Name</td>
<td>State</td>
</tr>
{{range .}}
<tr>
<td>{{.Name}}</td>
<td>{{.State}}</td>
</tr>
{{end}}
</table>
</tt>
</body>
</html>
`))

func goListHandler(w http.ResponseWriter, r *http.Request) {
	var i []goListInfo
	for _, g := range d.Goroutines {
		name := fmt.Sprintf("<a href=go?id=%x>goroutine %x</a>", g.Addr, g.Addr)
		var state string
		switch g.Status {
		case 0:
			state = "idle"
		case 1:
			state = "runnable"
		case 2:
			// running - shouldn't happen
			log.Fatal("found running goroutine in heap dump")
		case 3:
			state = "syscall"
		case 4:
			state = g.WaitReason
		case 5:
			state = "dead"
		default:
			log.Fatal("unknown goroutine status")
		}
		i = append(i, goListInfo{name, state})
	}
	// sort by state
	sort.Sort(ByState(i))
	if err := goListTemplate.Execute(w, i); err != nil {
		log.Print(err)
	}
}

type ByState []goListInfo

func (a ByState) Len() int           { return len(a) }
func (a ByState) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByState) Less(i, j int) bool { return a[i].State < a[j].State }

type goInfo struct {
	Addr   uint64
	State  string
	Frames []string
}

var goTemplate = template.Must(template.New("go").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Goroutine {{printf "%x" .Addr}}</title>
</head>
<body>
<tt>
<h2>Goroutine {{printf "%x" .Addr}}</h2>
<h3>{{.State}}</h3>
<h3>Stack</h3>
{{range .Frames}}
{{.}}
<br>
{{end}}
</tt>
</body>
</html>
`))

func goHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := q["id"]
	if len(v) != 1 {
		http.Error(w, "id parameter missing", 405)
		return
	}
	addr, err := strconv.ParseUint(v[0], 16, 64)
	if err != nil {
		http.Error(w, err.Error(), 405)
		return
	}
	var g *read.GoRoutine
	for _, x := range d.Goroutines {
		if x.Addr == addr {
			g = x
			break
		}
	}
	if g == nil {
		http.Error(w, "goroutine not found", 405)
		return
	}

	var i goInfo
	i.Addr = g.Addr
	switch g.Status {
	case 0:
		i.State = "idle"
	case 1:
		i.State = "runnable"
	case 2:
		// running - shouldn't happen
		log.Fatal("found running goroutine in heap dump")
	case 3:
		i.State = "syscall"
	case 4:
		i.State = g.WaitReason
	case 5:
		i.State = "dead"
	default:
		log.Fatal("unknown goroutine status")
	}

	for f := g.Bos; f != nil; f = f.Parent {
		i.Frames = append(i.Frames, fmt.Sprintf("<a href=frame?id=%x&depth=%d>%s</a>", f.Addr, f.Depth, f.Name))
	}

	if err := goTemplate.Execute(w, i); err != nil {
		log.Print(err)
	}
}

type frameInfo struct {
	Addr      uint64
	Name      string
	Depth     uint64
	Goroutine string
	Vars      []Field
}

var frameTemplate = template.Must(template.New("frame").Parse(`
<html>
<head>
<style>
table
{
border-collapse:collapse;
}
table, td, th
{
border:1px solid grey;
}
</style>
<title>Frame {{.Name}}</title>
</head>
<body>
<tt>
<h2>Frame {{.Name}}</h2>
<h3>In {{.Goroutine}}</h3>
<h3>Variables</h3>
<table>
<tr>
<td>Name</td>
<td>Type</td>
<td>Value</td>
</tr>
{{range .Vars}}
<tr>
<td>{{.Name}}</td>
<td>{{.Typ}}</td>
<td>{{.Value}}</td>
</tr>
{{end}}
</table>
</tt>
</body>
</html>
`))

func frameHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v := q["id"]
	if len(v) != 1 {
		http.Error(w, "id parameter missing", 405)
		return
	}
	addr, err := strconv.ParseUint(v[0], 16, 64)
	if err != nil {
		http.Error(w, err.Error(), 405)
		return
	}
	z := q["depth"]
	if len(z) != 1 {
		http.Error(w, "depth parameter missing", 405)
		return
	}
	depth, err := strconv.ParseUint(z[0], 10, 64)
	if err != nil {
		http.Error(w, err.Error(), 405)
		return
	}

	var f *read.StackFrame
	for _, g := range d.Frames {
		if g.Addr == addr && g.Depth == depth {
			f = g
			break
		}
	}
	if f == nil {
		http.Error(w, "stack frame not found", 405)
		return
	}

	var i frameInfo
	i.Addr = f.Addr
	i.Name = f.Name
	i.Depth = f.Depth
	i.Goroutine = fmt.Sprintf("<a href=go?id=%x>goroutine %x</a>", f.Goroutine.Addr, f.Goroutine.Addr)

	// variables
	i.Vars = getFields(f.Data, f.Fields, f.Edges)

	if err := frameTemplate.Execute(w, i); err != nil {
		log.Print(err)
	}
}

// So meta.
func heapdumpHandler(w http.ResponseWriter, r *http.Request) {
	f, err := os.Create("metadump")
	if err != nil {
		panic(err)
	}
	runtime.GC()
	debug.WriteHeapDump(f.Fd())
	f.Close()
	w.Write([]byte("done"))
}

func usage() {
	fmt.Fprintf(os.Stderr,
		"usage: hview heapdump [executable]\n")
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()

	fmt.Println("Loading...")
	args := flag.Args()
	if len(args) == 1 {
		d = read.Read(args[0], "")
	} else {
		d = read.Read(args[0], args[1])
	}

	fmt.Println("Analyzing...")
	prepare()

	fmt.Println("Ready.  Point your browser to localhost" + *httpAddr)
	http.HandleFunc("/", mainHandler)
	http.HandleFunc("/obj", objHandler)
	http.HandleFunc("/type", typeHandler)
	http.HandleFunc("/histo", histoHandler)
	http.HandleFunc("/globals", globalsHandler)
	http.HandleFunc("/goroutines", goListHandler)
	http.HandleFunc("/go", goHandler)
	http.HandleFunc("/frame", frameHandler)
	http.HandleFunc("/others", othersHandler)
	http.HandleFunc("/heapdump", heapdumpHandler)
	if err := http.ListenAndServe(*httpAddr, nil); err != nil {
		log.Fatal(err)
	}
}

// Map from object ID to list of objects that refer to the object.
// It is split in two parts for efficiency.  The first inbound
// reference is stored in ref1.  Any additional references are stored
// in ref2.  Since most objects have only one incoming reference,
// ref2 ends up small.
var ref1 []read.ObjId
var ref2 map[read.ObjId][]read.ObjId

func getReferrers(x read.ObjId) []string {
	var r []string
	if y := ref1[x]; y != read.ObjId(-1) {
		for _, e := range d.Edges(y) {
			if e.To == x {
				r = append(r, edgeSource(y, e))
			}
		}
	}
	for _, y := range ref2[x] {
		for _, e := range d.Edges(y) {
			if e.To == x {
				r = append(r, edgeSource(y, e))
			}
		}
	}
	for _, s := range []*read.Data{d.Data, d.Bss} {
		for _, e := range s.Edges {
			if e.To != x {
				continue
			}
			r = append(r, "global " + e.FieldName)
		}
	}
	for _, f := range d.Frames {
		for _, e := range f.Edges {
			if e.To == x {
				r = append(r, fmt.Sprintf("<a href=frame?id=%x&depth=%d>%s</a>.%s", f.Addr, f.Depth, f.Name, e.FieldName))
			}
		}
	}
	for _, s := range d.Otherroots {
		if s.E.To != x {
			continue
		}
		r = append(r, s.Description)
	}
	return r
}

type bucket struct {
	bytes   uint64
	objects []read.ObjId
}

// histogram by full type id
var byType []bucket

func prepare() {
	// group objects by type
	byType = make([]bucket, len(d.FTList))
	for i := range d.Objects {
		x := read.ObjId(i)
		tid := d.Ft(x).Id
		b := byType[tid]
		b.bytes += d.Size(x)
		b.objects = append(b.objects, x)
		byType[tid] = b
	}

	// compute referrers
	ref1 = make([]read.ObjId, len(d.Objects))
	for i := range d.Objects {
		ref1[i] = read.ObjId(-1)
	}
	ref2 = map[read.ObjId][]read.ObjId{}
	for i := range d.Objects {
		x := read.ObjId(i)
		for _, e := range d.Edges(x) {
			if ref1[e.To] < 0 {
				ref1[e.To] = x
			} else {
				ref2[e.To] = append(ref2[e.To], x)
			}
		}
	}
	fmt.Println("objects", len(d.Objects))
	fmt.Println("multirefer", len(ref2))

	s := uint64(0)
	for _, x := range d.Otherroots {
		s += d.Size(x.E.To)
	}
	fmt.Println("type data", s)
}

/*
func dom() {
	n := len(d.Objects)

	// compute postorder traversal
	// object states:
	// 0 - not seen yet
	// 1 - seen, added to queue
	// 2 - seen, already expanded children
	// 3 - added to postorder
	post := make([]*read.Object, 0, n)
	state := make(map[*read.Object]byte, n)
	var q []*read.Object // lifo queue, holds states 1 and 2
	for _, x := range d.Objects {
		if state[x] != 0 {
			continue
		}
		state[x] = 1
		q = q[:0]
		q = append(q, x)
		for len(q) > 0 {
			y := q[0]
			if state[y] == 2 {
				q = q[1:]
				post = append(post, y)
				// state[y] = 3: not really needed
			} else {
				state[y] = 2
				for _, e := range d.Edges(y) {
					z := e.To
					if state[z] == 0 {
						state[z] = 1
						q = append(q, z)
					}
				}
			}
		}
	}

	// compute dominance
	idom := make([]*read.Object, 0, n)
	done := false
	for !done {
		done = true
		for i := n - 1; i >= 0; i-- {
			x := post[i]
			for _, y := range d.Edges(x) { // TODO: reverse edges
				_ = y
			}
		}
	}
	_ = idom
}
*/

func readPtr(b []byte) uint64 {
	switch d.PtrSize {
	case 4:
		return read32(b)
	case 8:
		return read64(b)
	default:
		log.Fatal("unsupported PtrSize=%d", d.PtrSize)
		return 0
	}
}

func read64(b []byte) uint64 {
	switch {
	case d.Order == binary.LittleEndian:
		return uint64(b[0]) + uint64(b[1])<<8 + uint64(b[2])<<16 + uint64(b[3])<<24 + uint64(b[4])<<32 + uint64(b[5])<<40 + uint64(b[6])<<48 + uint64(b[7])<<56
	case d.Order == binary.BigEndian:
		return uint64(b[7]) + uint64(b[6])<<8 + uint64(b[5])<<16 + uint64(b[4])<<24 + uint64(b[3])<<32 + uint64(b[2])<<40 + uint64(b[1])<<48 + uint64(b[0])<<56
	default:
		log.Fatal("unsupported order=%v", d.Order)
		return 0
	}
}

func read32(b []byte) uint64 {
	switch {
	case d.Order == binary.LittleEndian:
		return uint64(b[0]) + uint64(b[1])<<8 + uint64(b[2])<<16 + uint64(b[3])<<24
	case d.Order == binary.BigEndian:
		return uint64(b[3]) + uint64(b[2])<<8 + uint64(b[1])<<16 + uint64(b[0])<<24
	default:
		log.Fatal("unsupported order=%v", d.Order)
		return 0
	}
}
func read16(b []byte) uint64 {
	switch {
	case d.Order == binary.LittleEndian:
		return uint64(b[0]) + uint64(b[1])<<8
	case d.Order == binary.BigEndian:
		return uint64(b[1]) + uint64(b[0])<<8
	default:
		log.Fatal("unsupported order=%v", d.Order)
		return 0
	}
}
