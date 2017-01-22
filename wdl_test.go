package maple

import (
	"path"
	"testing"
)

var root string = "test/parse"

func load(t *testing.T, path string) *WdlNamespace {
	ns, err := LoadWdlFromFile(path)
	if err != nil {
		t.Fatalf("%s", err)
	}
	return ns
}

func TestWdlNamespace0(t *testing.T) {
	t.Parallel()
	wdlPath := path.Join(root, "0.wdl")
	ns := load(t, wdlPath)
	if len(ns.namespaces) != 0 {
		t.Fatalf("%s: expecting 0 namespaces", wdlPath)
	}
	if len(ns.declarations) != 0 {
		t.Fatalf("%s: expecting 0 declarations", wdlPath)
	}
	if len(ns.workflows) != 1 {
		t.Fatalf("%s: expecting 1 workflows", wdlPath)
	}
	if len(ns.tasks) != 0 {
		t.Fatalf("%s: expecting 0 tasks", wdlPath)
	}
}

func TestWdlNamespace1(t *testing.T) {
	t.Parallel()
	wdlPath := path.Join(root, "1.wdl")
	ns := load(t, wdlPath)
	if len(ns.namespaces) != 0 {
		t.Fatalf("%s: expecting 0 namespaces", wdlPath)
	}
	if len(ns.declarations) != 0 {
		t.Fatalf("%s: expecting 0 declarations", wdlPath)
	}
	if len(ns.workflows) != 1 {
		t.Fatalf("%s: expecting 1 workflow", wdlPath)
	}
	if ns.workflows[0].name != "w" {
		t.Fatalf("%s: expecting workflow to have name 'w'", wdlPath)
	}
	if len(ns.workflows[0].body) != 1 {
		t.Fatalf("%s: expecting workflow 'w' to have one element in body")
	}
	if len(ns.tasks) != 1 {
		t.Fatalf("%s: expecting 1 task", wdlPath)
	}
}
