package conversation

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"testing"
)

type conversationSchema struct {
	Properties map[string]struct {
		Enum []string `json:"enum"`
	} `json:"properties"`
}

func TestContractEnumsMatchSchema(t *testing.T) {
	schemaBytes, err := os.ReadFile("../../../schemas/tank-conversation-event.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema conversationSchema
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		schemaProperty string
		goType         string
	}{
		{name: "actor", schemaProperty: "actor", goType: "Actor"},
		{name: "source", schemaProperty: "source", goType: "Source"},
		{name: "visibility", schemaProperty: "visibility", goType: "Visibility"},
		{name: "event type", schemaProperty: "type", goType: "EventType"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := schema.Properties[tt.schemaProperty].Enum
			if len(expected) == 0 {
				t.Fatalf("schema property %q has no enum", tt.schemaProperty)
			}
			actual := goStringConstants(t, tt.goType)
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("%s enum drift:\nGo:     %#v\nSchema: %#v", tt.name, actual, expected)
			}
		})
	}
}

func goStringConstants(t *testing.T, typeName string) []string {
	t.Helper()

	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, "types.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}

	var values []string
	for _, decl := range file.Decls {
		genericDecl, ok := decl.(*ast.GenDecl)
		if !ok || genericDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genericDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || !isIdentNamed(valueSpec.Type, typeName) {
				continue
			}
			for _, value := range valueSpec.Values {
				literal, ok := value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("%s constant has non-string value: %#v", typeName, value)
				}
				unquoted, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatal(err)
				}
				values = append(values, unquoted)
			}
		}
	}

	if len(values) == 0 {
		t.Fatalf("found no string constants for %s", typeName)
	}
	return values
}

func isIdentNamed(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}
