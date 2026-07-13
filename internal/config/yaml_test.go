package config

import (
	"reflect"
	"testing"
)

func TestParseYAMLEmpty(t *testing.T) {
	m, err := parseYAML([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

func TestParseYAMLSimpleMapping(t *testing.T) {
	input := []byte("name: web\naddress: \"0.0.0.0:8080\"")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["name"] != "web" {
		t.Errorf("name: got %v, want web", m["name"])
	}
	if m["address"] != "0.0.0.0:8080" {
		t.Errorf("address: got %v, want 0.0.0.0:8080", m["address"])
	}
}

func TestParseYAMLSequenceIndented(t *testing.T) {
	input := []byte("listeners:\n  - name: web\n    address: \"0.0.0.0:8080\"")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	listeners, ok := m["listeners"].([]interface{})
	if !ok {
		t.Fatalf("listeners: expected []interface{}, got %T", m["listeners"])
	}
	if len(listeners) != 1 {
		t.Fatalf("listeners: expected 1 item, got %d", len(listeners))
	}
	item, ok := listeners[0].(map[string]interface{})
	if !ok {
		t.Fatalf("listeners[0]: expected map, got %T", listeners[0])
	}
	if item["name"] != "web" {
		t.Errorf("name: got %v, want web", item["name"])
	}
	if item["address"] != "0.0.0.0:8080" {
		t.Errorf("address: got %v, want 0.0.0.0:8080", item["address"])
	}
}

func TestParseYAMLSequenceSameLevel(t *testing.T) {
	input := []byte("listeners:\n- name: web\n  address: \"0.0.0.0:8080\"")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	listeners, ok := m["listeners"].([]interface{})
	if !ok {
		t.Fatalf("listeners: expected []interface{}, got %T", m["listeners"])
	}
	if len(listeners) != 1 {
		t.Fatalf("listeners: expected 1 item, got %d", len(listeners))
	}
	item, ok := listeners[0].(map[string]interface{})
	if !ok {
		t.Fatalf("listeners[0]: expected map, got %T", listeners[0])
	}
	if item["name"] != "web" {
		t.Errorf("name: got %v, want web", item["name"])
	}
	if item["address"] != "0.0.0.0:8080" {
		t.Errorf("address: got %v, want 0.0.0.0:8080", item["address"])
	}
}

func TestParseYAMLListMultipleSameLevel(t *testing.T) {
	input := []byte("listeners:\n- name: web\n  address: \"0.0.0.0:8080\"\n- name: api\n  address: \"0.0.0.0:9090\"")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	listeners, ok := m["listeners"].([]interface{})
	if !ok {
		t.Fatalf("listeners: expected []interface{}, got %T", m["listeners"])
	}
	if len(listeners) != 2 {
		t.Fatalf("listeners: expected 2 items, got %d", len(listeners))
	}
	item0 := listeners[0].(map[string]interface{})
	item1 := listeners[1].(map[string]interface{})
	if item0["name"] != "web" || item0["address"] != "0.0.0.0:8080" {
		t.Errorf("item 0: got %v", item0)
	}
	if item1["name"] != "api" || item1["address"] != "0.0.0.0:9090" {
		t.Errorf("item 1: got %v", item1)
	}
}

func TestParseYAMLListMultipleIndented(t *testing.T) {
	input := []byte("listeners:\n  - name: web\n    address: \"0.0.0.0:8080\"\n  - name: api\n    address: \"0.0.0.0:9090\"")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	listeners, ok := m["listeners"].([]interface{})
	if !ok {
		t.Fatalf("listeners: expected []interface{}, got %T", m["listeners"])
	}
	if len(listeners) != 2 {
		t.Fatalf("listeners: expected 2 items, got %d", len(listeners))
	}
	item0 := listeners[0].(map[string]interface{})
	item1 := listeners[1].(map[string]interface{})
	if item0["name"] != "web" || item0["address"] != "0.0.0.0:8080" {
		t.Errorf("item 0: got %v", item0)
	}
	if item1["name"] != "api" || item1["address"] != "0.0.0.0:9090" {
		t.Errorf("item 1: got %v", item1)
	}
}

func TestParseYAMLBackendListSameLevel(t *testing.T) {
	input := []byte("backends:\n- address: \"10.0.0.1:8080\"\n  weight: 5\n- address: \"10.0.0.2:8080\"\n  weight: 3")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	backends, ok := m["backends"].([]interface{})
	if !ok {
		t.Fatalf("backends: expected []interface{}, got %T", m["backends"])
	}
	if len(backends) != 2 {
		t.Fatalf("backends: expected 2 items, got %d", len(backends))
	}
	b0 := backends[0].(map[string]interface{})
	b1 := backends[1].(map[string]interface{})
	if b0["address"] != "10.0.0.1:8080" {
		t.Errorf("b0 address: got %v", b0["address"])
	}
	if b0["weight"] != "5" {
		t.Errorf("b0 weight: got %v", b0["weight"])
	}
	if b1["address"] != "10.0.0.2:8080" {
		t.Errorf("b1 address: got %v", b1["address"])
	}
	if b1["weight"] != "3" {
		t.Errorf("b1 weight: got %v", b1["weight"])
	}
}

func TestParseYAMLBackendListIndented(t *testing.T) {
	input := []byte("backends:\n  - address: \"10.0.0.1:8080\"\n    weight: 5\n  - address: \"10.0.0.2:8080\"\n    weight: 3")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	backends, ok := m["backends"].([]interface{})
	if !ok {
		t.Fatalf("backends: expected []interface{}, got %T", m["backends"])
	}
	if len(backends) != 2 {
		t.Fatalf("backends: expected 2 items, got %d", len(backends))
	}
	b0 := backends[0].(map[string]interface{})
	b1 := backends[1].(map[string]interface{})
	if b0["address"] != "10.0.0.1:8080" {
		t.Errorf("b0 address: got %v", b0["address"])
	}
	if b0["weight"] != "5" {
		t.Errorf("b0 weight: got %v", b0["weight"])
	}
	if b1["address"] != "10.0.0.2:8080" {
		t.Errorf("b1 address: got %v", b1["address"])
	}
	if b1["weight"] != "3" {
		t.Errorf("b1 weight: got %v", b1["weight"])
	}
}

func TestParseYAMLPoolSameLevel(t *testing.T) {
	input := []byte("pools:\n- name: http_pool\n  algorithm: round_robin\n  backends:\n  - address: \"10.0.0.1:8080\"\n    weight: 1")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	pools, ok := m["pools"].([]interface{})
	if !ok {
		t.Fatalf("pools: expected []interface{}, got %T", m["pools"])
	}
	if len(pools) != 1 {
		t.Fatalf("pools: expected 1 item, got %d", len(pools))
	}
	pool := pools[0].(map[string]interface{})
	if pool["name"] != "http_pool" {
		t.Errorf("pool name: got %v", pool["name"])
	}
	if pool["algorithm"] != "round_robin" {
		t.Errorf("pool algorithm: got %v", pool["algorithm"])
	}
	backends, ok := pool["backends"].([]interface{})
	if !ok {
		t.Fatalf("pool backends: expected []interface{}, got %T", pool["backends"])
	}
	if len(backends) != 1 {
		t.Fatalf("pool backends: expected 1 item, got %d", len(backends))
	}
	b := backends[0].(map[string]interface{})
	if b["address"] != "10.0.0.1:8080" {
		t.Errorf("backend address: got %v", b["address"])
	}
}

func TestParseYAMLSameLevelScalarList(t *testing.T) {
	input := []byte("protocols:\n- tcp\n- udp\n- http")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	protocols, ok := m["protocols"].([]interface{})
	if !ok {
		t.Fatalf("protocols: expected []interface{}, got %T", m["protocols"])
	}
	expected := []interface{}{"tcp", "udp", "http"}
	if !reflect.DeepEqual(protocols, expected) {
		t.Errorf("protocols: got %v, want %v", protocols, expected)
	}
}

func TestParseYAMLInlineValue(t *testing.T) {
	input := []byte("name: web\naddress: \"0.0.0.0:8080\"")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["name"] != "web" {
		t.Errorf("name: got %v, want web", m["name"])
	}
}

func TestParseYAMLNullValue(t *testing.T) {
	input := []byte("name: web\naddress:")
	m, err := parseYAML(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["name"] != "web" {
		t.Errorf("name: got %v, want web", m["name"])
	}
	if m["address"] != "" {
		t.Errorf("address: got %v, want empty string", m["address"])
	}
}
