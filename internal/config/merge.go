package config

// Save merges a Config into the node tree of the file already on disk
// instead of marshalling from scratch, so the comments, key order and
// scalar styles a person wrote survive a UI save. This file is that merge.
// What yaml.v3 can't round-trip is lost on the first real change: blank
// lines, the column an end-of-line comment sits in, where a comment at the
// end of a block is indented, the extra nesting of a hand-written 4-space
// file, and plain URLs inside a flow-style list (its emitter quotes them).
// A new key lands after the nearest key cfg already had, which can put it
// above the comment that documents it (the example's commented-out
// preprocess block hangs off trigger); moving that comment would be a
// guess about which key it describes, so it stays where it is.

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// fileSchema is what the merge knows about the file beyond its bytes: every
// key Load reads, by dotted path (sequences add no segment, so a watch's op
// is "watches.trigger.op"), with the Go type it decodes into; and, for the
// keys Validate defaults to something other than zero, the value a missing
// key reads as. Both come from the structs and from Validate itself, so
// neither can drift from the merge.
type fileSchema struct {
	types    map[string]reflect.Type
	defaults map[string]string
}

var schema = buildSchema()

func buildSchema() fileSchema {
	s := fileSchema{types: map[string]reflect.Type{}, defaults: map[string]string{}}
	walkType(reflect.TypeOf(Config{}), "", s.types)
	// Validate's defaults, read off a minimal config before and after it runs.
	bare := Config{
		MQTT: &MQTT{Broker: "tcp://x"},
		Watches: []Watch{{
			Name: "a", Source: "http://x",
			Region:  Region{W: 1, H: 1},
			Trigger: Trigger{Type: "pixel_change", Threshold: 1},
		}},
	}
	before := scalars(mustEncode(&bare), "", map[string]string{})
	if err := bare.Validate(); err != nil {
		panic("config: schema template rejected: " + err.Error())
	}
	for p, v := range scalars(mustEncode(&bare), "", map[string]string{}) {
		if before[p] != v {
			s.defaults[p] = v
		}
	}
	return s
}

// walkType records every yaml-tagged field under t by dotted path with its
// declared type, looking through pointers and slices to find the structs
// to descend into. It refuses at init what the merge could not merge — a
// map or interface field, whose keys it would take for a person's and keep
// after cfg dropped them, and an inline struct, whose keys it would file
// under the wrong path — so such a field fails the first test run rather
// than the first save.
func walkType(t reflect.Type, prefix string, out map[string]reflect.Type) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if _, opts, _ := strings.Cut(f.Tag.Get("yaml"), ","); slices.Contains(strings.Split(opts, ","), "inline") {
			panic("config: " + t.Name() + "." + f.Name + " is inline; merge.go files keys by field and would put its keys at the wrong path")
		}
		ft := elem(f.Type)
		if k := ft.Kind(); k == reflect.Map || k == reflect.Interface {
			panic("config: " + t.Name() + "." + f.Name + " is a " + k.String() + "; merge.go only merges struct, slice and scalar fields, and would keep entries cfg had removed")
		}
		name := prefix + yamlName(f)
		out[name] = f.Type
		if ft.Kind() == reflect.Struct {
			walkType(ft, name+".", out)
		}
	}
}

// yamlName is the key a struct field is read from.
func yamlName(f reflect.StructField) string {
	if name, _, _ := strings.Cut(f.Tag.Get("yaml"), ","); name != "" {
		return name
	}
	return strings.ToLower(f.Name) // yaml.v3's default for an untagged field
}

// withDefaults gives a decoded value the defaults Validate would, so two
// values compare the way Load reads them: a trigger written without
// confirm and one written with confirm: 3 are the same trigger.
func withDefaults(v reflect.Value, path string) {
	switch v.Kind() {
	case reflect.Pointer:
		if !v.IsNil() {
			withDefaults(v.Elem(), path)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			withDefaults(v.Index(i), path)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f, kp := v.Field(i), join(path, yamlName(v.Type().Field(i)))
			if d, ok := schema.defaults[kp]; ok && f.IsZero() {
				_ = yaml.Unmarshal([]byte(d), f.Addr().Interface())
			}
			withDefaults(f, kp)
		}
	}
}

// elem is t with pointers and slices peeled off: the type one item of a
// field decodes into.
func elem(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t
}

// scalars flattens a node tree into path -> scalar value.
func scalars(n *yaml.Node, path string, out map[string]string) map[string]string {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			scalars(n.Content[i+1], join(path, n.Content[i].Value), out)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			scalars(c, path, out)
		}
	case yaml.ScalarNode:
		out[path] = n.Value
	}
	return out
}

func mustEncode(v any) *yaml.Node {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		panic("config: encode schema template: " + err.Error())
	}
	return &n
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// regionPath is the one mapping every example writes whole and on one line.
const regionPath = "watches.region"

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// mergeFile returns the bytes to write for cfg given the file's current
// contents, or nil when the file already says exactly that. The bytes keep
// the file's byte order mark, line endings, document-start marker and
// sequence layout, and are read back through Load's own path before they
// are returned: whatever the merge does, it never hands Save something Load
// would reject or read differently from cfg.
func mergeFile(raw []byte, cfg *Config) ([]byte, error) {
	bom := bytes.HasPrefix(raw, utf8BOM)
	if bom {
		raw = raw[len(utf8BOM):]
	}
	// yaml.v3's scanner drops and splits comments on CRLF input, so the
	// merge works on LF and puts the CRLF back on the way out.
	crlf := bytes.Contains(raw, []byte("\r\n")) && bytes.Count(raw, []byte("\n")) == bytes.Count(raw, []byte("\r\n"))
	if crlf {
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	}
	doc, err := parseFile(raw)
	if err != nil {
		return nil, err
	}
	root := doc.Content[0]
	normalize(root)
	var desired yaml.Node
	if err := desired.Encode(cfg); err != nil {
		return nil, err
	}
	indent := detectIndent(raw)
	compact := compactSequences(root)
	before, err := encodeDoc(doc, indent)
	if err != nil {
		return nil, err
	}
	m := newMerger(root)
	m.merge(root, &desired, "")
	m.fix(doc, "", map[*yaml.Node]bool{})
	after, err := encodeDoc(doc, indent)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(before, after) {
		return nil, nil
	}
	if compact {
		after = dedentSequences(after, indent)
	}
	after = keepDocStart(raw, after)
	if crlf {
		after = bytes.ReplaceAll(after, []byte("\n"), []byte("\r\n"))
	}
	if bom {
		after = append(append([]byte{}, utf8BOM...), after...)
	}
	if err := loadsBack(after, cfg); err != nil {
		return nil, err
	}
	return after, nil
}

// parseFile is the file as a document whose root is a mapping. A file that
// is empty or nothing but comments (Load reads both as an empty config)
// becomes an empty mapping under those comments; anything else Load could
// not read is an error, so a save never replaces a file someone is halfway
// through editing by hand.
func parseFile(raw []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse existing config: %w", err)
	}
	if doc.Kind == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, HeadComment: commentBlock(raw)}
		return &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}}, nil
	}
	if len(doc.Content) != 1 {
		return nil, errors.New("parse existing config: expected a single document")
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" { // "---" or "~": an empty config
		*root = yaml.Node{
			Kind:        yaml.MappingNode,
			HeadComment: joinComments(root.HeadComment, root.LineComment),
			FootComment: root.FootComment,
		}
	}
	if root.Kind != yaml.MappingNode {
		return nil, errors.New("parse existing config: top level is not a mapping")
	}
	return &doc, nil
}

// commentBlock is the comment lines of a file that holds nothing else,
// blank lines between them kept, as one head comment.
func commentBlock(raw []byte) string {
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "#"):
			lines = append(lines, t)
		case t == "" && len(lines) > 0:
			lines = append(lines, "")
		}
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// normalize tidies what the parser hands us before either encoding: the
// !!merge tag it puts on a "<<" key would be spelled out on re-emit, and a
// comment that follows a sequence item at the dash column, with a blank
// line after it, is hung on the NEXT item's first key, where the emitter
// would print it inside that item — it belongs after the item it followed,
// and goes with that item if that item is deleted.
func normalize(n *yaml.Node) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if isMergeKey(n.Content[i]) {
				n.Content[i].Tag = ""
			}
			normalize(n.Content[i+1])
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			if i > 0 && item.Kind == yaml.MappingNode && len(item.Content) > 0 && item.Content[0].FootComment != "" {
				if prev := n.Content[i-1]; prev.Kind == yaml.MappingNode && len(prev.Content) > 0 {
					last := prev.Content[len(prev.Content)-2]
					last.FootComment = joinComments(last.FootComment, item.Content[0].FootComment)
					item.Content[0].FootComment = ""
				}
			}
			normalize(item)
		}
	}
}

// detectIndent is the indent of the first indented line that isn't a
// comment, or 2 when nothing is.
func detectIndent(raw []byte) int {
	for _, line := range strings.Split(string(raw), "\n") {
		body := strings.TrimLeft(line, " ")
		if n := len(line) - len(body); n > 0 && strings.TrimSpace(body) != "" && !strings.HasPrefix(body, "#") {
			return n
		}
	}
	return 2
}

func encodeDoc(doc *yaml.Node, indent int) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(indent)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// merger carries what one merge has to remember across the walk: what each
// merge-keyed mapping ("<<: *defaults") inherited before anything changed,
// and the aliases and merge-keyed mappings it met, which are settled in fix
// once every anchor's final value is known.
type merger struct {
	inh     map[*yaml.Node]map[string]*yaml.Node
	wanted  map[*yaml.Node]target
	pending map[*yaml.Node]target
}

type target struct {
	de   *yaml.Node
	path string
}

func newMerger(root *yaml.Node) *merger {
	m := &merger{
		inh:     map[*yaml.Node]map[string]*yaml.Node{},
		wanted:  map[*yaml.Node]target{},
		pending: map[*yaml.Node]target{},
	}
	var snapshot func(n *yaml.Node)
	snapshot = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.MappingNode:
			if inh := inherited(n); inh != nil {
				m.inh[n] = map[string]*yaml.Node{}
				for _, e := range inh {
					m.inh[n][e.key] = dealias(e.val, 0)
				}
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				snapshot(n.Content[i+1])
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				snapshot(c)
			}
		}
	}
	snapshot(root)
	return m
}

// merge rewrites ex, the file's node, to say what de, the freshly encoded
// one, says, touching as little as it can. It returns the node to keep: ex,
// or de when the two are different kinds.
func (m *merger) merge(ex, de *yaml.Node, path string) *yaml.Node {
	if ex.Kind == yaml.AliasNode {
		// Whether the alias still reads as de depends on what its anchor
		// says once the whole tree is merged; fix decides.
		m.pending[ex] = target{de, path}
		return ex
	}
	if ex.Kind != de.Kind {
		// A bare "notify:" already reads as an empty list.
		if ex.Kind == yaml.ScalarNode && ex.Tag == "!!null" && de.Kind != yaml.ScalarNode && len(de.Content) == 0 {
			return ex
		}
		prune(de, path)
		de.HeadComment, de.LineComment, de.FootComment = ex.HeadComment, ex.LineComment, ex.FootComment
		return de
	}
	switch ex.Kind {
	case yaml.MappingNode:
		m.mergeMapping(ex, de, path)
	case yaml.SequenceNode:
		if t := schema.types[path]; t != nil && elem(t).Kind() == reflect.Struct {
			m.mergeWatches(ex, de, path)
		} else {
			mergeList(ex, de)
		}
	case yaml.ScalarNode:
		mergeScalar(ex, de, path)
	}
	return ex
}

func (m *merger) mergeMapping(ex, de *yaml.Node, path string) {
	if len(ex.Content) == 0 {
		ex.Style = de.Style
	}
	// inh is what a merge key gives this mapping for the keys it leaves
	// out: leaving one out then means reading that, not the default.
	inh := m.inh[ex]
	if inh != nil {
		m.wanted[ex] = target{de, path}
	}
	// A new key goes after the last key that matched, or last of all when
	// none has yet: in front of the first key it would push the file's
	// header comment, which hangs off that key, down the file.
	at := -1
	for i := 0; i+1 < len(de.Content); i += 2 {
		k, v := de.Content[i], de.Content[i+1]
		kp := join(path, k.Value)
		if j := indexOf(ex, k.Value); j >= 0 {
			if clears(ex.Content[j+1], v, kp) && !shadows(inh, k.Value, kp) {
				continue // dropped below
			}
			ex.Content[j+1] = m.merge(ex.Content[j+1], v, kp)
			hoistLineComment(ex.Content[j], ex.Content[j+1])
			at = j + 2
			continue
		}
		if n := inh[k.Value]; n != nil {
			if readsSame(n, v, kp) {
				continue
			}
		} else if absentEquivalent(v, kp) {
			continue
		}
		prune(v, kp)
		k.Style = 0
		if at < 0 {
			at = len(ex.Content)
		}
		ex.Content = slices.Insert(ex.Content, at, k, v)
		at += 2
	}
	// Known keys cfg no longer has (an omitempty field back at zero, a
	// removed auth/mqtt block) or has cleared go; anything else is the
	// user's and stays. A key that shadows a non-zero inherited value
	// stays, spelling the zero out, or Load would read the inherited value
	// instead. The file's header comment hangs off its first key, so it
	// moves to the next key when that one goes.
	kept := ex.Content[:0]
	var header string
	for i := 0; i+1 < len(ex.Content); i += 2 {
		k, v := ex.Content[i], ex.Content[i+1]
		if kp := join(path, k.Value); schema.types[kp] != nil {
			if j := indexOf(de, k.Value); j < 0 || clears(v, de.Content[j+1], kp) {
				if !shadows(inh, k.Value, kp) {
					if i == 0 {
						header = k.HeadComment
					}
					continue
				}
				v = zeroNode(kp)
			}
		}
		if header != "" {
			k.HeadComment = joinComments(header, k.HeadComment)
			header = ""
		}
		kept = append(kept, k, v)
	}
	ex.Content = kept
	if header != "" {
		ex.HeadComment = joinComments(header, ex.HeadComment)
	}
	// Inherited keys neither side spells out, where cfg's zero would read
	// as the inherited value: those get the explicit zero.
	if inh != nil {
		for _, key := range sortedKeys(inh) {
			kp := join(path, key)
			if schema.types[kp] == nil || indexOf(ex, key) >= 0 || indexOf(de, key) >= 0 || isZero(inh[key], kp) {
				continue
			}
			ex.Content = append(ex.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, zeroNode(kp))
		}
	}
}

// clears reports whether de sets a scalar the file spells out to its zero
// value. The line then goes rather than becoming pattern: "" or a stale
// threshold: 0 (the UI blanks what a trigger type doesn't read), except in
// region, where a 0 is a coordinate.
func clears(ex, de *yaml.Node, path string) bool {
	return de.Kind == yaml.ScalarNode && !strings.HasPrefix(path, regionPath+".") &&
		isZero(de, path) && !readsSame(ex, de, path)
}

// shadows reports whether leaving key out of a mapping would make Load
// read something other than zero for it: a merge key supplies it.
func shadows(inh map[string]*yaml.Node, key, path string) bool {
	n := inh[key]
	return n != nil && !isZero(n, path)
}

// hoistLineComment moves an end-of-line comment off a block collection,
// which yaml.v3 would print after the next "- " of the enclosing list,
// onto its key: "notify: # comment" above the items. It also undoes that
// when the collection has been emptied: yaml.v3 prints an empty block
// collection under a commented key as "[]" alone at column 0 on the next
// line, which does not parse, so the empty value goes back on the key's
// line as flow, comment after it: "watches: [] # comment".
func hoistLineComment(k, v *yaml.Node) {
	if (v.Kind != yaml.MappingNode && v.Kind != yaml.SequenceNode) || v.Style&yaml.FlowStyle != 0 {
		return
	}
	switch {
	case len(v.Content) > 0 && v.LineComment != "":
		k.LineComment = joinComments(k.LineComment, v.LineComment)
		v.LineComment = ""
	case len(v.Content) == 0 && k.LineComment != "":
		v.Style |= yaml.FlowStyle
		v.LineComment = joinComments(k.LineComment, v.LineComment)
		k.LineComment = ""
	}
}

// mergeWatches matches the file's watches to cfg's by name, so an edited
// watch keeps its comments. The result is in cfg's order: new watches
// appended, watches cfg no longer has dropped with their own comments —
// but a comment block above a dropped watch (a commented-out watch parked
// there, say) is not its own, and moves to the next watch that stays.
func (m *merger) mergeWatches(ex, de *yaml.Node, path string) {
	byName := map[string]int{}
	for i, item := range ex.Content {
		if n := valueOf(item, "name"); n != nil {
			if _, dup := byName[n.Value]; !dup {
				byName[n.Value] = i
			}
		}
	}
	if len(ex.Content) == 0 {
		ex.Style = de.Style
	}
	used := make([]bool, len(ex.Content))
	out := make([]*yaml.Node, 0, len(de.Content))
	for _, want := range de.Content {
		if n := valueOf(want, "name"); n != nil {
			if j, ok := byName[n.Value]; ok && !used[j] {
				used[j] = true
				out = append(out, m.merge(ex.Content[j], want, path))
				continue
			}
		}
		prune(want, path)
		out = append(out, want)
	}
	carryDropped(ex, used, out)
	ex.Content = out
}

// mergeList is for lists of scalars (notify): an item the file has keeps
// its node, comments and quoting, new ones are appended fresh, removed ones
// go, and the result is in cfg's order under the list's own comments.
func mergeList(ex, de *yaml.Node) {
	if len(ex.Content) == 0 {
		ex.Style = de.Style
	}
	used := make([]bool, len(ex.Content))
	out := make([]*yaml.Node, 0, len(de.Content))
	same := len(ex.Content) == len(de.Content)
	for i, want := range de.Content {
		j := -1
		for h, have := range ex.Content {
			if !used[h] && have.Kind == yaml.ScalarNode && have.Value == want.Value {
				j = h
				break
			}
		}
		if j < 0 {
			same = false
			out = append(out, want)
			continue
		}
		used[j] = true
		same = same && i == j
		out = append(out, ex.Content[j])
	}
	if same {
		return
	}
	carryDropped(ex, used, out)
	ex.Content = out
}

// carryDropped keeps the comments an item was only hosting: the block
// above a dropped item goes to the next item that stays in file order (or
// after the last one, when none does), and a comment under the last item
// stays at the end of the list when that item is dropped or another is
// appended after it.
func carryDropped(seq *yaml.Node, used []bool, out []*yaml.Node) {
	var head string
	for j, item := range seq.Content {
		if !used[j] {
			head = joinComments(head, item.HeadComment)
			continue
		}
		if head != "" {
			item.HeadComment = joinComments(head, item.HeadComment)
			head = ""
		}
	}
	if n := len(seq.Content); n > 0 && (len(out) == 0 || out[len(out)-1] != seq.Content[n-1]) {
		head = joinComments(head, seq.Content[n-1].FootComment)
		seq.Content[n-1].FootComment = ""
	}
	switch {
	case head == "":
	case len(out) > 0:
		out[len(out)-1].FootComment = joinComments(out[len(out)-1].FootComment, head)
	default:
		seq.FootComment = joinComments(head, seq.FootComment)
	}
}

// mergeScalar leaves a scalar alone when Load already reads cfg's value
// from it ("80.0" for 80, "60s" for 1m0s, a quoted string) and otherwise
// sets the new value, keeping the quotes on a string the user quoted.
func mergeScalar(ex, de *yaml.Node, path string) {
	if readsSame(ex, de, path) {
		return
	}
	quoted := ex.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0
	if !quoted || de.Tag != "!!str" {
		ex.Style = de.Style
	}
	ex.Value, ex.Tag = de.Value, de.Tag
}

// readsSame reports whether Load reads the same value at path from a as
// from b — through aliases and merge keys, with Validate's defaults
// filled in, and with nil and empty lists the same list, which is what
// yaml.Marshal normalizes and DeepEqual would not.
func readsSame(a, b *yaml.Node, path string) bool {
	t := schema.types[path]
	if t == nil {
		return a.Kind == b.Kind && a.Value == b.Value
	}
	x, y := reflect.New(t), reflect.New(t)
	if a.Decode(x.Interface()) != nil || b.Decode(y.Interface()) != nil {
		return false
	}
	withDefaults(x.Elem(), path)
	withDefaults(y.Elem(), path)
	if reflect.DeepEqual(x.Elem().Interface(), y.Elem().Interface()) {
		return true
	}
	xb, errX := yaml.Marshal(x.Interface())
	yb, errY := yaml.Marshal(y.Interface())
	return errX == nil && errY == nil && bytes.Equal(xb, yb)
}

// absentEquivalent reports whether Load reads the same thing from a file
// without this key as from this node: Validate's default for it, or zero.
func absentEquivalent(v *yaml.Node, path string) bool {
	if d, ok := schema.defaults[path]; ok && v.Value == d {
		return true
	}
	return isZero(v, path)
}

// isZero reports whether the node reads as the field's zero value: an
// empty collection, a zero scalar, or an alias to either.
func isZero(v *yaml.Node, path string) bool {
	t := schema.types[path]
	if t == nil {
		return v.Kind != yaml.ScalarNode && len(v.Content) == 0
	}
	p := reflect.New(t)
	if v.Decode(p.Interface()) != nil {
		return v.Kind != yaml.ScalarNode && len(v.Content) == 0
	}
	e := p.Elem()
	return e.IsZero() || (e.Kind() == reflect.Slice && e.Len() == 0)
}

// zeroNode is the field's zero value spelled out: 0s, [], {}, null.
func zeroNode(path string) *yaml.Node {
	var n yaml.Node
	if err := n.Encode(reflect.Zero(schema.types[path]).Interface()); err != nil {
		panic("config: encode zero for " + path + ": " + err.Error())
	}
	return &n
}

// prune makes a freshly encoded node read as though a person wrote it: keys
// Load fills in the same way when missing are dropped (a new watch gets no
// pattern: "", cooldown: 0s or confirm: 3), keys are plain (the encoder
// quotes "y" for YAML 1.1 parsers we don't have), and region goes whole on
// one line — a zero x or y there is a coordinate, not an omission.
func prune(n *yaml.Node, path string) {
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			prune(c, path)
		}
	case yaml.MappingNode:
		if path == regionPath {
			n.Style = yaml.FlowStyle
		}
		kept := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			kp := join(path, k.Value)
			if path != regionPath && absentEquivalent(v, kp) {
				continue
			}
			k.Style = 0
			prune(v, kp)
			kept = append(kept, k, v)
		}
		n.Content = kept
	}
}

// --- merge keys and aliases ---------------------------------------------

// isMergeKey mirrors yaml.v3's own test for a "<<" key.
func isMergeKey(k *yaml.Node) bool {
	return k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "" || k.Tag == "!" || k.Tag == "!!merge")
}

type kv struct {
	key string
	val *yaml.Node
}

// inherited is what a mapping reads through its merge keys for the keys it
// doesn't spell out, in yaml.v3's order of precedence: the mapping's own
// keys win, then the first source to supply a key, sources read in document
// order and through their own merge keys. nil when there is no merge key.
func inherited(m *yaml.Node) []kv {
	own := map[string]bool{}
	hasMerge := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isMergeKey(m.Content[i]) {
			hasMerge = true
		} else {
			own[m.Content[i].Value] = true
		}
	}
	if !hasMerge {
		return nil
	}
	var out []kv
	var from func(n *yaml.Node, depth int)
	from = func(n *yaml.Node, depth int) {
		if depth > 32 {
			return
		}
		switch n.Kind {
		case yaml.AliasNode:
			if n.Alias != nil {
				from(n.Alias, depth+1)
			}
		case yaml.SequenceNode:
			for _, c := range n.Content {
				from(c, depth+1)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if isMergeKey(k) {
					from(v, depth+1)
					continue
				}
				if !own[k.Value] {
					own[k.Value] = true
					out = append(out, kv{k.Value, v})
				}
			}
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if isMergeKey(m.Content[i]) {
			from(m.Content[i+1], 0)
		}
	}
	return out
}

// dealias is a deep copy of n with every alias replaced by a copy of what
// it points at and no anchors, so it reads the same wherever it is put.
func dealias(n *yaml.Node, depth int) *yaml.Node {
	if n == nil {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	if n.Kind == yaml.AliasNode && depth <= 32 {
		return dealias(n.Alias, depth+1)
	}
	c := *n
	c.Anchor = ""
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, x := range n.Content {
		c.Content[i] = dealias(x, depth+1)
	}
	return &c
}

// fix settles what the merge could only decide once the whole tree was
// final: whether an alias still reads as cfg says, whether a merge-keyed
// mapping still inherits what cfg says, and whether every alias still has
// its anchor ahead of it in the file. One that doesn't — its watch was
// deleted, renamed or moved below it — is replaced by a copy of what it
// pointed at, so the file never carries a reference Load can't resolve.
func (m *merger) fix(n *yaml.Node, path string, seen map[*yaml.Node]bool) {
	if n.Anchor != "" {
		seen[n] = true
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			m.fix(c, path, seen)
		}
	case yaml.MappingNode:
		if w, ok := m.wanted[n]; ok {
			settleInherited(n, w)
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if isMergeKey(k) {
				if !anchorsAhead(v, seen) {
					materialize(n)
					i -= 2 // the copies that replaced it hold no aliases; visiting them is harmless
				}
				continue
			}
			m.fix(k, path, seen)
			m.fix(v, join(path, k.Value), seen)
			hoistLineComment(k, v)
		}
	case yaml.AliasNode:
		ahead := n.Alias != nil && seen[n.Alias]
		if p, ok := m.pending[n]; ok {
			if !ahead || !readsSame(n.Alias, p.de, p.path) {
				prune(p.de, p.path)
				replace(n, p.de)
			}
		} else if !ahead {
			replace(n, dealias(n.Alias, 0))
		}
	}
}

// settleInherited writes out, in a merge-keyed mapping, every inherited key
// whose final inherited value is not what cfg says — the anchor it comes
// from may have been edited in the same save.
func settleInherited(n *yaml.Node, w target) {
	for _, e := range inherited(n) {
		kp := join(w.path, e.key)
		if schema.types[kp] == nil || indexOf(n, e.key) >= 0 {
			continue
		}
		var v *yaml.Node
		switch want := valueOf(w.de, e.key); {
		case want == nil && !isZero(e.val, kp):
			v = zeroNode(kp)
		case want != nil && !readsSame(e.val, want, kp):
			prune(want, kp)
			v = want
		default:
			continue
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: e.key}, v)
	}
}

// anchorsAhead reports whether every alias a merge key's value refers to
// has its anchor earlier in the file.
func anchorsAhead(v *yaml.Node, seen map[*yaml.Node]bool) bool {
	switch v.Kind {
	case yaml.AliasNode:
		return v.Alias != nil && seen[v.Alias]
	case yaml.SequenceNode:
		for _, c := range v.Content {
			if !anchorsAhead(c, seen) {
				return false
			}
		}
	}
	return true
}

// materialize replaces a mapping's merge keys with the keys they supplied,
// copied in, under the merge key's own comment.
func materialize(n *yaml.Node) {
	var ins []*yaml.Node
	for _, e := range inherited(n) {
		ins = append(ins, &yaml.Node{Kind: yaml.ScalarNode, Value: e.key}, dealias(e.val, 0))
	}
	kept := make([]*yaml.Node, 0, len(n.Content)+len(ins))
	var head string
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if !isMergeKey(k) {
			if head != "" {
				k.HeadComment = joinComments(head, k.HeadComment)
				head = ""
			}
			kept = append(kept, k, v)
			continue
		}
		head = joinComments(head, k.HeadComment)
		if ins != nil {
			ins[0].HeadComment = joinComments(head, ins[0].HeadComment)
			head = ""
			kept = append(kept, ins...)
			ins = nil
		}
	}
	n.Content = kept
	if head != "" {
		n.HeadComment = joinComments(n.HeadComment, head)
	}
}

// replace turns n into with in place, under n's own comments.
func replace(n, with *yaml.Node) {
	head, line, foot := n.HeadComment, n.LineComment, n.FootComment
	*n = *with
	if head != "" {
		n.HeadComment = head
	}
	if line != "" {
		n.LineComment = line
	}
	if foot != "" {
		n.FootComment = foot
	}
}

// --- layout the encoder can't keep ---------------------------------------

// compactSequences reports whether the file writes block sequences at their
// key's column ("watches:\n- name: a"), the way yq, kubectl and ansible do,
// judged by the first one it has.
func compactSequences(n *yaml.Node) bool {
	compact, _ := compactIn(n)
	return compact
}

func compactIn(n *yaml.Node) (compact, found bool) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
				return k.Column == v.Column && k.Line < v.Line, true
			}
			if c, ok := compactIn(v); ok {
				return c, ok
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c, ok := compactIn(c); ok {
				return c, ok
			}
		}
	}
	return false, false
}

// dedentSequences moves every block sequence the encoder indented under
// its key back to the key's column. Everything from the line after the key
// to the first line indented less than the items belongs to the sequence,
// and shifts as one, nested sequences shifting once per level.
func dedentSequences(out []byte, indent int) []byte {
	var doc yaml.Node
	if yaml.Unmarshal(out, &doc) != nil {
		return out
	}
	lines := strings.Split(string(out), "\n")
	shift := make([]int, len(lines))
	var visit func(n *yaml.Node)
	visit = func(n *yaml.Node) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				visit(c)
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				k, v := n.Content[i], n.Content[i+1]
				if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 && len(v.Content) > 0 {
					min := k.Column - 1 + indent
					for l := k.Line; l < len(lines); l++ {
						if strings.TrimSpace(lines[l]) == "" {
							continue
						}
						if leading(lines[l]) < min {
							break
						}
						shift[l] += indent
					}
				}
				visit(v)
			}
		}
	}
	visit(&doc)
	for l, s := range lines {
		if leading(s) < shift[l] {
			return out
		}
		lines[l] = s[shift[l]:]
	}
	return []byte(strings.Join(lines, "\n"))
}

func leading(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// keepDocStart puts back the "---" line the encoder drops, where the file
// had it: first of all, or after its header comment.
func keepDocStart(raw, out []byte) []byte {
	marker := []byte("---\n")
	sawComment := false
	for _, l := range strings.Split(string(raw), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
		case strings.HasPrefix(t, "#"):
			sawComment = true
		case t != "---":
			return out
		case !sawComment:
			return append(marker, out...)
		default:
			lines := bytes.SplitAfter(out, []byte("\n"))
			for i, ln := range lines {
				if s := strings.TrimSpace(string(ln)); s != "" && !strings.HasPrefix(s, "#") {
					return bytes.Join([][]byte{bytes.Join(lines[:i], nil), marker, bytes.Join(lines[i:], nil)}, nil)
				}
			}
			return out
		}
	}
	return out
}

// loadsBack is the last line of defence: the bytes about to be written
// must read back, through Load's own path, as exactly the config that was
// saved. A config that doesn't validate can't be checked that way and is
// only required to parse.
func loadsBack(out []byte, cfg *Config) error {
	var got Config
	if err := yaml.Unmarshal(out, &got); err != nil {
		return fmt.Errorf("merged config would not parse: %w", err)
	}
	want := cfg.clone()
	if err := want.Validate(); err != nil {
		return nil
	}
	if err := got.Validate(); err != nil {
		return fmt.Errorf("merged config would not validate: %w", err)
	}
	if !reflect.DeepEqual(want.normalized(), got.normalized()) {
		return errors.New("merged config would not read back as saved")
	}
	return nil
}

// clone is a copy Validate can default without touching c.
func (c *Config) clone() *Config {
	out := *c
	out.Watches = slices.Clone(c.Watches)
	if c.Auth != nil {
		a := *c.Auth
		out.Auth = &a
	}
	if c.MQTT != nil {
		m := *c.MQTT
		out.MQTT = &m
	}
	return &out
}

// normalized is c with nil and empty notify lists, which are the same list
// on disk, made the same for reflect.DeepEqual.
func (c *Config) normalized() Config {
	out := *c
	out.Watches = nil
	for _, w := range c.Watches {
		if len(w.Notify) == 0 {
			w.Notify = nil
		}
		out.Watches = append(out.Watches, w)
	}
	return out
}

// --- small helpers ---------------------------------------------------------

func joinComments(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}

func sortedKeys(m map[string]*yaml.Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func indexOf(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func valueOf(m *yaml.Node, key string) *yaml.Node {
	if i := indexOf(m, key); i >= 0 {
		return m.Content[i+1]
	}
	return nil
}
