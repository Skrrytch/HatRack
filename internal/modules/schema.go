package modules

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func validateAgainst(schema []byte, value any, where string) error {
	if len(schema) == 0 {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("%s: bad module schema: %w", where, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("mem://module.json", doc); err != nil {
		return err
	}
	sch, err := c.Compile("mem://module.json")
	if err != nil {
		return fmt.Errorf("%s: bad module schema: %w", where, err)
	}
	// Round trip through JSON so that YAML-decoded numbers and maps are plain.
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	if err := sch.Validate(inst); err != nil {
		msg := strings.ReplaceAll(err.Error(), "\n", "; ")
		return fmt.Errorf("%s: %s", where, msg)
	}
	return nil
}
