package maple

import (
	"fmt"
	"io"
	"io/ioutil"
	"regexp"
	"strings"
)

type Graph struct {
	nodes []*Node
}

func (g *Graph) Add(node *Node) {
	g.nodes = append(g.nodes, node)
}

type Node struct {
	in   []string
	name string
	out  []string
}

func (n *Node) String() string {
	return fmt.Sprintf("[Node name=%s in=%s out=%s]", n.name, n.in, n.out)
}

func (g *Graph) Find(name string) *Node {
	for _, node := range g.nodes {
		if node != nil && node.name == name {
			return node
		}
	}
	return nil
}

func (g *Graph) Root() []*Node {
	root := make([]*Node, 0)
	for _, node := range g.nodes {
		if len(g.Upstream(node)) == 0 {
			root = append(root, node)
		}
	}
	return root
}

func (g *Graph) Upstream(n *Node) []*Node {
	upstream := make([]*Node, 0)
	for _, input := range n.in {
		for _, node := range g.nodes {
			if node != nil && node.name == input {
				upstream = append(upstream, node)
			}
		}
	}
	return upstream
}

func (g *Graph) Downstream(n *Node) []*Node {
	downstream := make([]*Node, 0)
	for _, node := range g.nodes {
		for _, node2 := range g.Upstream(node) {
			if node2 == n {
				downstream = append(downstream, node)
			}
		}
	}
	return downstream
}

func LoadGraph(reader io.Reader) *Graph {
	bytes, _ := ioutil.ReadAll(reader)
	lines := strings.Split(string(bytes), "\n")
	graph := Graph{make([]*Node, 0)}
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		re := regexp.MustCompile("\\[([a-zA-Z0-9,]*)\\]([a-zA-Z0-9]+)\\[([a-zA-Z0-9,]*)\\]")
		parsed := re.FindStringSubmatch(line)
		in := strings.Split(parsed[1], ",")
		name := parsed[2]
		out := strings.Split(parsed[3], ",")
		node := Node{in, name, out}
		graph.Add(&node)
	}
	return &graph
}
