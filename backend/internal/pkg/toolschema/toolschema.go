// Package toolschema 编译并校验工具参数 JSON Schema。外部 $ref 与超大文档一律拒绝。
package toolschema

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type noSchemaLoader struct{}

func (noSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external schema references are disabled")
}

func Compile(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 || len(raw) > 32768 {
		return nil, errors.New("tool schema too large or absent")
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	obj, ok := doc.(map[string]any)
	if !ok || obj["type"] != "object" {
		return nil, errors.New("tool schema must describe an object")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noSchemaLoader{})
	if err = compiler.AddResource("https://campus.invalid/tool-schema", doc); err != nil {
		return nil, err
	}
	return compiler.Compile("https://campus.invalid/tool-schema")
}

func Validate(raw json.RawMessage, args string) (map[string]any, error) {
	if len(args) > 8192 {
		return nil, errors.New("arguments too large")
	}
	schema, err := Compile(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	var doc any
	if err = decoder.Decode(&doc); err != nil {
		return nil, err
	}
	if err = decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errors.New("arguments contain trailing data")
	}
	if err = schema.Validate(doc); err != nil {
		return nil, err
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be object")
	}
	return m, nil
}
