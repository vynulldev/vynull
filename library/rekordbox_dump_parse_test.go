// SPDX-License-Identifier: GPL-3.0-or-later
package library

import (
	"encoding/json"
	"os/exec"
	"testing"
)

// The dump helper reads SmartList XML and numeric columns as rekordbox does
// (link-spec smart-xml-matrix and smart-numeric-boundaries, rekordbox
// 7.2.19). Runs the helper's pure functions under python3 with sqlcipher3
// stubbed out, so no database or key is needed.
func TestDumpHelperParsesLikeRekordbox(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	const script = `
import importlib.util, json, sys, types
sys.modules["sqlcipher3"] = types.ModuleType("sqlcipher3")
spec = importlib.util.spec_from_file_location("dump", "../tools/rekordbox_dump.py")
dump = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dump)
house = '<CONDITION PropertyName="genre" Operator="1" ValueUnit="" ValueLeft="Fixture House" ValueRight=""/>'
canonical = '<NODE Id="1" LogicalOperator="1">' + house + '</NODE>'
def rule(x):
    r = dump.parse_smartlist(x, {})
    if r is None:
        return None
    return [r["logical"]] + [c["operator"] for c in r["conditions"]] + [c["left"] for c in r["conditions"]]
out = {
    "canonical": rule(canonical),
    "lowercase": rule(canonical.replace("NODE", "node").replace("CONDITION", "condition")),
    "mismatched_close": rule(canonical.replace("</NODE>", "</NODES>")),
    "two_roots": rule(canonical + canonical.replace("House", "Techno")),
    "trailing": rule(canonical + "trailing"),
    "nul_junk": rule(canonical + "\0junk"),
    "duplicate_value": rule(canonical.replace('ValueLeft="Fixture House"', 'ValueLeft="Fixture House" ValueLeft="Fixture Techno"')),
    "duplicate_logical": rule(canonical.replace('LogicalOperator="1"', 'LogicalOperator="2" LogicalOperator="1"')),
    "invalid_logical": rule(canonical.replace('LogicalOperator="1"', 'LogicalOperator="wat"')),
    "entity": rule(canonical.replace("Fixture House", "Fixture&#x20;House")),
    "bom": rule("\ufeff" + canonical),
    "leading_text": rule("leading" + canonical),
    "unclosed": rule(canonical[:-len("</NODE>")]),
    "unknown_entity": rule(canonical.replace("Fixture House", "Fixture &bogus; House")),
    "wrapper_root": rule("<ROOT>" + canonical + "</ROOT>"),
    "ints": [dump.db_int(v) for v in (None, 0.5, 1.5, -0.5, -1, 2**31, 2**32)],
    "u16": [dump.db_int(v, 16, False) for v in (-1, 2**31 - 1, 2**31, 2**32 - 1, 2**32)],
    "s8": [dump.db_int(v, 8) for v in (-1, 2**31 - 1, 2**31, 1.5)],
}
json.dump(out, sys.stdout)
`
	raw, err := exec.Command("python3", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v\n%s", err, raw)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	want := map[string]string{
		"canonical":         `[1,1,"Fixture House"]`,
		"lowercase":         `[1,1,"Fixture House"]`,
		"mismatched_close":  `[1,1,"Fixture House"]`,
		"two_roots":         `[1,1,"Fixture House"]`,
		"trailing":          `[1,1,"Fixture House"]`,
		"nul_junk":          `[1,1,"Fixture House"]`,
		"duplicate_value":   `[1,1,"Fixture House"]`,
		"duplicate_logical": `[2,1,"Fixture House"]`,
		"invalid_logical":   `[1,1,"Fixture House"]`,
		"entity":            `[1,1,"Fixture House"]`,
		"bom":               `null`,
		"leading_text":      `null`,
		"unclosed":          `null`,
		"unknown_entity":    `null`,
		"wrapper_root":      `[1]`,
		"ints":              `[0,0,1,0,-1,-2147483648,0]`,
		"u16":               `[65535,65535,0,65535,0]`,
		"s8":                `[-1,-1,0,1]`,
	}
	for k, w := range want {
		g, _ := json.Marshal(got[k])
		if string(g) != w {
			t.Errorf("%s = %s, want %s", k, g, w)
		}
	}
}

// The helper resolves a My Tag condition's value as rekordbox reads it: a
// signed 32-bit integer attribute whose bits are the tag ID, saturating past
// the signed range, reading malformed text as tag 0 and a leading number
// from anything else (link-spec smart-mytag-matrix, rekordbox 7.2.19).
func TestDumpHelperReadsMyTagValuesLikeRekordbox(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	const script = `
import importlib.util, json, sys, types
sys.modules["sqlcipher3"] = types.ModuleType("sqlcipher3")
spec = importlib.util.spec_from_file_location("dump", "../tools/rekordbox_dump.py")
dump = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dump)
tags = {"0": "Zero", "1": "One", "2147483647": "Max", "2147483648": "High", "4294967295": "All"}
def tag(value):
    xml = '<NODE Id="1" LogicalOperator="1"><CONDITION PropertyName="myTag" Operator="8" ValueUnit="" ValueLeft="%s" ValueRight=""/></NODE>' % value
    return dump.parse_smartlist(xml, tags)["conditions"][0]["left"]
values = ["1", "2147483647", "2147483648", "4294967295", "4294967296", "-2147483648", "-1",
          "-2147483649", " 001", "+1", " 1 ", "0x1", "1,2147483648", "wat", "", "12345"]
json.dump({v: tag(v) for v in values}, sys.stdout)
`
	raw, err := exec.Command("python3", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("python3: %v\n%s", err, raw)
	}
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	for value, want := range map[string]string{
		"1": "One", "2147483647": "Max",
		// Past the signed range saturates to its top, not the bits above it.
		"2147483648": "Max", "4294967295": "Max", "4294967296": "Max",
		// Negative values carry the high bit; underflow saturates to it.
		"-2147483648": "High", "-1": "All", "-2147483649": "High",
		" 001": "One", "+1": "One", " 1 ": "One",
		"0x1": "Zero", "1,2147483648": "One", "wat": "Zero",
		"":      "",      // blank stays blank and matches nothing
		"12345": "12345", // no such tag
	} {
		if got[value] != want {
			t.Errorf("ValueLeft %q resolved to %q, want %q", value, got[value], want)
		}
	}
}
