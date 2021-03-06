package sdiffer

import (
	"fmt"
	. "reflect"
	"regexp"
	"strconv"
	"strings"
)

type diffMode int

const (
	ignoreMode diffMode = iota
	includeMode
	allDiffMode
)

const (
	initTypeName        = "$"
	null                = "<nil>"
	notNull             = "<not nil>"
	useComparatorSuffix = ".$[customized]"
	defaultDepthLimit   = 30
)

// Differ compares two interfaces with the same reflect.Type.
//
// For example:
// differ := NewDiffer().Ignore(`xxx`, `xxx`).Compare(a, b)
//
// Attention:
// Differ may cause panic when you call Compare.
type Differ struct {
	diffs       map[string]*diff
	ignores     []*regexp.Regexp
	includes    []*regexp.Regexp
	trimSpaces  []*regexp.Regexp
	trimTags    []*trimTag
	comparators []Comparator
	sorters     []Sorter
	maxDepth    int
	diffTmpl    string
	bff         *bufferF
}

func NewDiffer() *Differ {
	return &Differ{
		diffs:    make(map[string]*diff, 16),
		bff:      newBufferF(),
		maxDepth: defaultDepthLimit,
	}
}

func (d *Differ) String() string {
	for _, df := range d.diffs {
		d.bff.sprintf("%s\n", df.String(d.diffTmpl))
	}
	return d.bff.String()
}

func (d *Differ) Diffs() []*diff {
	dfs := make([]*diff, 0, len(d.diffs))
	for _, df := range d.diffs {
		dfs = append(dfs, df)
	}
	return dfs
}

// WithMaxDepth set the max depth of Differ.
// Differ will panic if depth is over max depth when comparing.
func (d *Differ) WithMaxDepth(depth int) *Differ {
	d.maxDepth = depth
	return d
}

// WithTmpl set diff tmpl for Differ.
// Tmpl must contains exactly 3 placeholders, such as:
// `Field: "%s", A: %v, B: %v`
func (d *Differ) WithTmpl(tmpl string) *Differ {
	d.diffTmpl = tmpl
	return d
}

// Ignore set fields that do not need to be compared.
// Ignore will not work after Includes is called.
func (d *Differ) Ignore(regexps ...string) *Differ {
	if len(d.includes) > 0 {
		return d
	}
	d.ignores = make([]*regexp.Regexp, 0, len(regexps))
	for _, expr := range regexps {
		d.ignores = append(d.ignores, regexp.MustCompile(expr))
	}
	return d
}

// Includes set fields that need to be compared.
// Ignore will not work after Includes is called.
func (d *Differ) Includes(regexps ...string) *Differ {
	d.includes = make([]*regexp.Regexp, 0, len(regexps))
	for _, expr := range regexps {
		d.includes = append(d.includes, regexp.MustCompile(expr))
	}
	return d
}

// WithComparator specify some fields to compare with a customized Comparator.
func (d *Differ) WithComparator(c Comparator) *Differ {
	d.comparators = append(d.comparators, c)
	return d
}

// WithSorter sort some fields to do disordered comparison.
func (d *Differ) WithSorter(s Sorter) *Differ {
	d.sorters = append(d.sorters, s)
	return d
}

// WithTrim trim string before comparison.
func (d *Differ) WithTrim(fieldPath string, cutset string) *Differ {
	d.trimTags = append(d.trimTags, newTrimTag(fieldPath, cutset))
	return d
}

// WithTrimSpace trim space before comparison.
func (d *Differ) WithTrimSpace(fieldPaths ...string) *Differ {
	for _, exp := range fieldPaths {
		d.trimSpaces = append(d.trimSpaces, regexp.MustCompile(exp))
	}
	return d
}

// FindDiff find diff with name.
func (d *Differ) FindDiff(fieldName string) (df *diff, ok bool) {
	df, ok = d.diffs[fieldName]
	return
}

// FindDiffFuzzily find diff with regexp.
func (d *Differ) FindDiffFuzzily(expr string) (dfs []*diff) {
	if r, err := regexp.Compile(expr); err == nil {
		for name, df := range d.diffs {
			if r.MatchString(name) {
				dfs = append(dfs, df)
			}
		}
	}
	return
}

func (d *Differ) Reset() *Differ {
	d.includes = make([]*regexp.Regexp, 0, len(d.includes))
	d.ignores = make([]*regexp.Regexp, 0, len(d.ignores))
	d.trimSpaces = make([]*regexp.Regexp, 0, len(d.trimSpaces))
	d.trimTags = make([]*trimTag, 0, len(d.trimTags))
	d.comparators = make([]Comparator, 0, len(d.comparators))
	d.sorters = make([]Sorter, 0, len(d.sorters))
	d.diffs = make(map[string]*diff, len(d.diffs))
	d.bff = newBufferF()
	return d
}

func (d *Differ) Compare(a, b interface{}) *Differ {
	va, vb := ValueOf(a), ValueOf(b)
	if va.Type() != vb.Type() {
		typeMismatchPanic(a, b)
	}
	tName := va.Type().Name()
	if va.Kind() == Ptr {
		tName = va.Elem().Type().Name()
	}
	d.doCompare(va, vb, iF(isStringBlank(tName), initTypeName, tName).(string), 0)
	return d
}

func (d *Differ) doCompare(a, b Value, fieldPath string, depth int) {
	if depth > d.maxDepth {
		panic("depth over limit")
	}

	if !a.IsValid() || !b.IsValid() {
		panic("value invalid: " + a.Type().String())
	}

	if a.Type() != b.Type() {
		typeMismatchPanic(a.Type(), b.Type())
	}

	for _, c := range d.comparators {
		if c.Match(fieldPath) {
			fieldPath = fieldPath + useComparatorSuffix
			dt, va, vb := c.Equals(a.Interface(), b.Interface())
			switch dt {
			case LengthDiff:
				d.setLenDiff(fieldPath, a, b)
			case NilDiff:
				d.setNilDiff(fieldPath, a, b)
			case ElemDiff:
				d.setDiff(fieldPath, va, vb)
			case NoDiff:
				return
			default:
				panic("customized comparator returned an unexpected DiffType")
			}
			return
		}
	}

	switch a.Kind() {
	case Array:
		for i := 0; i < minInt(a.Len(), b.Len()); i++ {
			d.doCompare(a.Index(i), b.Index(i), a.Index(i).Type().Name(), depth)
		}
	case Slice:
		if a.IsNil() != b.IsNil() {
			d.setNilDiff(fieldPath, a, b)
			return
		}
		if a.Len() != b.Len() {
			d.setLenDiff(fieldPath, a, b)
		}
		if a.Pointer() == b.Pointer() {
			return
		}
		for _, s := range d.sorters {
			if s.Match(fieldPath) {
				a, b = d.sortSlice(a, b, s)
				break
			}
		}
		for i := 0; i < minInt(a.Len(), b.Len()); i++ {
			d.doCompare(a.Index(i), b.Index(i),
				concat(fieldPath, "[", strconv.Itoa(i), "]"), depth)
		}
	case Interface:
		if a.IsNil() != b.IsNil() {
			d.setNilDiff(fieldPath, a, b)
			return
		}

		if sa, sb, ok := parseStringValue(a, b); ok {
			d.doCompare(sa, sb, fieldPath, depth)
			return
		}

		if fa, fb, ok := parseFloatValue(a, b); ok {
			d.doCompare(fa, fb, fieldPath, depth)
			return
		}

		if ba, bb, ok := parseBoolValue(a, b); ok {
			d.doCompare(ba, bb, fieldPath, depth)
			return
		}

		if aa, ab, ok := parseArrayValue(a, b); ok {
			d.doCompare(aa, ab, fieldPath, depth)
			return
		}

		if ma, mb, ok := parseMapValue(a, b); ok {
			d.doCompare(ma, mb, fieldPath, depth+1)
			return
		}

		panic(fmt.Sprintf("unexpected interface with type: %s", a.Type().Name()))

	case Ptr:
		if a.IsNil() != b.IsNil() {
			d.setNilDiff(fieldPath, a, b)
			return
		}
		if a.Pointer() != b.Pointer() {
			d.doCompare(a.Elem(), b.Elem(), fieldPath, depth)
		}
	case Struct:
		for i, n := 0, a.NumField(); i < n; i++ {
			d.doCompare(a.Field(i), b.Field(i), concat(fieldPath, ".", a.Type().Field(i).Name), depth+1)
		}
	case Map:
		if a.IsNil() != b.IsNil() {
			d.setNilDiff(fieldPath, a, b)
			return
		}
		if a.Len() != b.Len() {
			d.setLenDiff(fieldPath, a, b)
		}
		for _, k := range a.MapKeys() {
			v1, v2 := a.MapIndex(k), b.MapIndex(k)
			d.doCompare(v1, v2, concat(fieldPath, "[", toString(k.Interface()), "]"), depth)
		}
	case String:
		for _, ts := range d.trimSpaces {
			if ts.MatchString(fieldPath) {
				if !DeepEqual(strings.TrimSpace(a.String()), strings.TrimSpace(b.String())) {
					d.setDiff(fieldPath, a, b)
				}
				return
			}
		}
		for _, tt := range d.trimTags {
			if tt.fieldRegexp.MatchString(fieldPath) {
				if !DeepEqual(tt.Trim(a.String()), tt.Trim(b.String())) {
					d.setDiff(fieldPath, a, b)
				}
				return
			}
		}
		fallthrough
	default:
		if !DeepEqual(a.Interface(), b.Interface()) {
			d.setDiff(fieldPath, a, b)
			return
		}
	}
}

func (d *Differ) sortSlice(sa, sb Value, sorter Sorter) (sortedSa, sortedSb Value) {
	// deep copy slice to avoid affect the original data.
	sortedSa = copySliceValue(sa)
	sortedSb = copySliceValue(sb)
	qsort(sortedSa, sorter.Less)
	qsort(sortedSb, sorter.Less)
	return
}

func (d *Differ) setNilDiff(fieldName string, a, b Value) {
	d.setDiff(fieldName, iF(a.IsNil(), null, notNull), iF(b.IsNil(), null, notNull))
}

func (d *Differ) setLenDiff(fieldName string, a, b Value) {
	d.setDiff(fieldName+"[Length]", a.Len(), b.Len())
}

func (d *Differ) setDiff(fieldName string, va, vb interface{}) {
	switch d.getDiffMode() {
	case includeMode:
		if !d.isIncludedField(fieldName) {
			return
		}
	case ignoreMode:
		if d.isIgnoredField(fieldName) {
			return
		}
	}
	d.diffs[fieldName] = newDiff(fieldName, va, vb)
}

func (d *Differ) getDiffMode() diffMode {
	if len(d.includes) > 0 {
		return includeMode
	}
	if len(d.ignores) > 0 {
		return ignoreMode
	}
	return allDiffMode
}

func (d *Differ) isIncludedField(fieldName string) bool {
	for _, ic := range d.includes {
		if ic.MatchString(fieldName) {
			return true
		}
	}
	return false
}

func (d *Differ) isIgnoredField(fieldName string) bool {
	for _, ig := range d.ignores {
		if ig.MatchString(fieldName) {
			return true
		}
	}
	return false
}

func typeMismatchPanic(a, b interface{}) {
	panic("type mismatch: " + newDiff("type", a, b).String())
}
