package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"testing"
)

type videoRouterCall struct {
	position token.Pos
	method   string
	args     []string
}

func TestVideoV1CreationRoutesValidateHealthProbePinBeforeDistribution(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "video-router.go", nil, 0)
	if err != nil {
		t.Fatalf("parse video router: %v", err)
	}

	var calls []videoRouterCall
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != "videoV1Router" {
			return true
		}

		entry := videoRouterCall{position: call.Pos(), method: selector.Sel.Name}
		for _, argument := range call.Args {
			entry.args = append(entry.args, videoRouterArgumentName(argument))
		}
		calls = append(calls, entry)
		return true
	})
	sort.Slice(calls, func(i, j int) bool { return calls[i].position < calls[j].position })

	var middlewareOrder []string
	var creationPaths []string
	for _, call := range calls {
		switch call.method {
		case "Use":
			middlewareOrder = append(middlewareOrder, call.args...)
		case "POST":
			if len(call.args) > 0 {
				creationPaths = append(creationPaths, call.args[0])
			}
		}
	}

	wantPaths := []string{"/video/generations", "/videos/:video_id/remix", "/videos"}
	if !reflect.DeepEqual(creationPaths, wantPaths) {
		t.Fatalf("video creation routes=%v, want %v", creationPaths, wantPaths)
	}

	wantOrder := []string{"RouteTag", "TokenAuth", "ZTAPIHealthProbePin", "Distribute"}
	if !reflect.DeepEqual(middlewareOrder, wantOrder) {
		t.Fatalf("videoV1Router middleware order=%v, want %v", middlewareOrder, wantOrder)
	}
}

func videoRouterArgumentName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.BasicLit:
		return value.Value[1 : len(value.Value)-1]
	case *ast.CallExpr:
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			return selector.Sel.Name
		}
	case *ast.SelectorExpr:
		return value.Sel.Name
	}
	return ""
}
