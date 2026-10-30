package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// A dashboard's panels are a JSON string. Two strings that describe the same
// panels are equal whatever their spacing, key order or empty fields, so
// the JSON Visionary answers with never shows up as a change.

type panel struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Ref         string            `json:"ref"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	Field       string            `json:"field,omitempty"`
	Viz         json.RawMessage   `json:"viz,omitempty"`
	X           int               `json:"x"`
	Y           int               `json:"y"`
	W           int               `json:"w"`
	H           int               `json:"h"`
}

func parsePanels(s string) ([]panel, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	var out []panel
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("panels must be a JSON array of {id, kind, ref, x, y, w, h, ...}: %w", err)
	}
	if out == nil {
		return nil, errors.New("panels must be a JSON array; use [] for none")
	}
	for i, p := range out {
		if p.ID == "" || p.Ref == "" || (p.Kind != "saved" && p.Kind != "builtin") {
			return nil, fmt.Errorf("panel %d needs an id, a kind (saved or builtin) and a ref", i+1)
		}
	}
	return out, nil
}

// canonicalPanels writes panels the one way they compare: empty fields left
// out and keys sorted, as jsonencode() writes them.
func canonicalPanels(list []panel) (string, error) {
	for i := range list {
		v := list[i].Viz
		if len(v) == 0 || string(v) == "null" {
			list[i].Viz = nil
			continue
		}
		var x any
		if err := json.Unmarshal(v, &x); err != nil {
			return "", err
		}
		b, err := json.Marshal(x)
		if err != nil {
			return "", err
		}
		list[i].Viz = b
	}
	b, err := json.Marshal(list)
	if err != nil {
		return "", err
	}
	var sorted any
	if err := json.Unmarshal(b, &sorted); err != nil {
		return "", err
	}
	b, err = json.Marshal(sorted)
	return string(b), err
}

func samePanels(a, b string) bool {
	pa, err1 := parsePanels(a)
	pb, err2 := parsePanels(b)
	if err1 != nil || err2 != nil {
		return false
	}
	ca, err1 := canonicalPanels(pa)
	cb, err2 := canonicalPanels(pb)
	return err1 == nil && err2 == nil && ca == cb
}

type panelsType struct{ basetypes.StringType }

var (
	_ basetypes.StringTypable                    = panelsType{}
	_ basetypes.StringValuableWithSemanticEquals = panelsValue{}
)

func (t panelsType) String() string { return "panelsType" }

func (t panelsType) Equal(o attr.Type) bool {
	_, ok := o.(panelsType)
	return ok
}

func (t panelsType) ValueType(context.Context) attr.Value { return panelsValue{} }

func (t panelsType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return panelsValue{StringValue: in}, nil
}

func (t panelsType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	v, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	s, ok := v.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T", v)
	}
	return panelsValue{StringValue: s}, nil
}

type panelsValue struct{ basetypes.StringValue }

func (v panelsValue) Type(context.Context) attr.Type { return panelsType{} }

func (v panelsValue) Equal(o attr.Value) bool {
	other, ok := o.(panelsValue)
	return ok && v.StringValue.Equal(other.StringValue)
}

func (v panelsValue) StringSemanticEquals(_ context.Context, newValue basetypes.StringValuable) (bool, diag.Diagnostics) {
	n, ok := newValue.(panelsValue)
	if !ok {
		return false, nil
	}
	return samePanels(v.ValueString(), n.ValueString()), nil
}

func panelsOf(s string) panelsValue { return panelsValue{StringValue: basetypes.NewStringValue(s)} }
