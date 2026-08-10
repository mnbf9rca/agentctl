package skills

import (
	"bytes"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestFleetTemplateSchemaCompiles(t *testing.T) {
	const schemaPath = Root + "/references/fleet-template.schema.json"

	schema, err := Tree.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", schemaPath, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatalf("UnmarshalJSON(%q): %v", schemaPath, err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaPath, document); err != nil {
		t.Fatalf("AddResource(%q): %v", schemaPath, err)
	}
	if _, err := compiler.Compile(schemaPath); err != nil {
		t.Fatalf("Compile(%q): %v", schemaPath, err)
	}
}
