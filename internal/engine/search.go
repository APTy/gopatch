package engine

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"

	"github.com/uber-go/gopatch/internal/data"
	"golang.org/x/tools/go/ast/astutil"
)

// SearchResult contains information about search results found by a
// SearchMatcher.
type SearchResult struct {
	// Object containing the matched node.
	parent ast.Node

	// Name of the field of the parent referring to the matched node.
	name string

	// Non-negative if parent.name is a slice. The match node is at this
	// index in parent.name.
	index int

	data   data.Data
	region Region
}

// SearchNode provides access to an AST node, its parent, and its positional
// information during a traversal.
type SearchNode interface {
	Node() ast.Node
	Parent() ast.Node
	Name() string
	Index() int
}

// Searcher inspects the given Node using the given Matcher and returns a
// non-nil SearchResult if it matched.
type Searcher func(SearchNode, Matcher, data.Data) *SearchResult

// Traverses the AST, calling the provided matcher on nodes of the provided
// type.
func searchAST(nodeType reflect.Type) Searcher {
	return func(cursor SearchNode, m Matcher, d data.Data) *SearchResult {
		v := reflect.ValueOf(cursor.Node())
		if !v.Type().AssignableTo(nodeType) {
			return nil
		}

		d, ok := m.Match(v, d, nodeRegion(cursor.Node()))
		if !ok {
			return nil
		}

		return &SearchResult{
			parent: cursor.Parent(),
			name:   cursor.Name(),
			index:  cursor.Index(),
			data:   data.Index(d),
			region: nodeRegion(cursor.Node()),
		}
	}
}

// SearchMatcher runs a Matcher on descendants of an AST, producing
// SearchResults into Data.
//
// The corresponding replacer applies a Replacer to these matched descendants.
type SearchMatcher struct {
	Search  Searcher
	Matcher Matcher
}

// Match runs the matcher on the provided ast.Node.
func (m SearchMatcher) Match(got reflect.Value, d data.Data, _ Region) (data.Data, bool) {
	n, ok := got.Interface().(ast.Node)
	if !ok {
		return d, false
	}

	var results []*SearchResult
	astutil.Apply(n, func(cursor *astutil.Cursor) bool {
		n := cursor.Node()
		if n == nil {
			return false
		}

		if r := m.Search(cursor, m.Matcher, d); r != nil {
			results = append(results, r)
			return false
		}

		return true // keep looking
	}, nil /* post func */)

	return pushSearchResults(d, got, results), len(results) > 0
}

// SearchReplacer replaces nodes found by a SearchMatcher.
type SearchReplacer struct {
	Replacer Replacer
}

// Replace replaces nodes found by a SearchMatcher.
func (r SearchReplacer) Replace(d data.Data, cl Changelog, pos token.Pos) (reflect.Value, error) {
	root, results := lookupSearchResults(d)
	if len(results) == 0 {
		return root, nil
	}

	for _, m := range results {
		v := reflect.Indirect(reflect.ValueOf(m.parent)).FieldByName(m.name)
		if !v.IsValid() {
			// This is a bug in our code.
			panic(fmt.Sprintf("%q is not a field of %T", m.name, m.parent))
		}

		if m.index >= 0 {
			v = v.Index(m.index)
		}

		give, err := r.Replacer.Replace(m.data, cl, m.region.Pos)
		if err != nil {
			return reflect.Value{}, err
		}

		// If the generated value isn't assignable to the target, the match
		// was too eager. For example, trying to place "foo.Bar"
		// (SelectorExpr) where only an identifier is allowed (in a variable
		// declaration name, for example).
		if give.Type().AssignableTo(v.Type()) {
			v.Set(give)
		}
	}

	return root, nil
}

type _searchResultKey struct{}

var searchResultKey _searchResultKey

type searchResultData struct {
	Root    reflect.Value
	Results []*SearchResult
}

func pushSearchResults(d data.Data, root reflect.Value, results []*SearchResult) data.Data {
	return data.WithValue(d, searchResultKey, searchResultData{
		Root:    root,
		Results: results,
	})
}

func lookupSearchResults(d data.Data) (root reflect.Value, results []*SearchResult) {
	var sr searchResultData
	_ = data.Lookup(d, searchResultKey, &sr) // TODO(abg): Handle !ok
	return sr.Root, sr.Results
}