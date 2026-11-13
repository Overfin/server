// Package api carries Overfin's API specification (openapi.yaml), so the
// gateway can pass on exactly the routes it lists: its allowlist is the
// specification of the same release (SECURITY.md, the gateway's
// allowlist). apps.yaml, the apps' API, is the backend's only, on its LAN
// listener: never the gateway's.
package api

import (
	"bufio"
	_ "embed"
	"regexp"
	"strings"
)

//go:embed openapi.yaml
var spec string

var (
	specPath   = regexp.MustCompile(`^  (/[^:]*):\s*$`)
	specMethod = regexp.MustCompile(`^    (get|put|post|patch|delete):\s*$`)
)

// Operation is one route of the API: a method and a path under /api/v1,
// with its parameters in braces ({titleId}).
type Operation struct {
	Method, Path string
	Planned      bool // listed, not served yet (x-status: planned)
	Public       bool // needs no session (security: [])
}

// Operations are the routes the specification lists.
func Operations() []Operation {
	var ops []Operation
	path := ""
	inPaths := false
	sc := bufio.NewScanner(strings.NewReader(spec))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "paths:":
			inPaths = true
		case inPaths && line != "" && line[0] != ' ' && line[0] != '#':
			inPaths = false
		case !inPaths:
		case specPath.MatchString(line):
			path = specPath.FindStringSubmatch(line)[1]
		case specMethod.MatchString(line) && path != "":
			ops = append(ops, Operation{Method: strings.ToUpper(specMethod.FindStringSubmatch(line)[1]), Path: "/api/v1" + path})
		case len(ops) > 0 && strings.TrimSpace(line) == "x-status: planned":
			ops[len(ops)-1].Planned = true
		case len(ops) > 0 && line == "      security: []":
			ops[len(ops)-1].Public = true
		}
	}
	return ops
}
