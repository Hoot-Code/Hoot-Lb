package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestExpositionCounterHelpType(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("http_requests_total", "Total HTTP requests")
	c := r.Entries()[0]

	var buf bytes.Buffer
	if err := writeHelpAndType(&buf, c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "# HELP http_requests_total Total HTTP requests") {
		t.Fatalf("missing HELP line: %s", out)
	}
	if !strings.Contains(out, "# TYPE http_requests_total counter") {
		t.Fatalf("missing TYPE line: %s", out)
	}
}

func TestExpositionHistogramBuckets(t *testing.T) {
	r := NewRegistry()
	h := r.NewHistogram("request_duration_seconds", "Request duration", DefaultHistogramBuckets)
	h.Observe(0.003)
	h.Observe(0.05)
	h.Observe(100.0)

	var buf bytes.Buffer
	if err := WriteExposition(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.Contains(out, `request_duration_seconds_bucket{le="+Inf"}`) {
		t.Fatalf("missing +Inf bucket:\n%s", out)
	}
	if !strings.Contains(out, "request_duration_seconds_sum") {
		t.Fatalf("missing sum line:\n%s", out)
	}
	if !strings.Contains(out, "request_duration_seconds_count 3") {
		t.Fatalf("missing count line:\n%s", out)
	}
	// Check cumulative bucket for 0.05: observations <= 0.05 are 0.003 and 0.05 = 2
	if !strings.Contains(out, `request_duration_seconds_bucket{le="0.05"} 2`) {
		t.Fatalf("missing cumulative bucket for 0.05:\n%s", out)
	}
}

func TestExpositionLabelEscaping(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("test_total", "test", []string{"label"})
	cv.With(`value"with\backslash`).Add(1)

	var buf bytes.Buffer
	if err := WriteExposition(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `label="value\"with\\backslash"`) {
		t.Fatalf("label not properly escaped:\n%s", out)
	}
}

func TestExpositionDeterministic(t *testing.T) {
	r := NewRegistry()
	cv := r.NewCounterVec("test_total", "test", []string{"a", "b"})
	cv.With("x", "y").Add(1)
	cv.With("a", "b").Add(2)

	var buf1, buf2 bytes.Buffer
	WriteExposition(&buf1, r)
	WriteExposition(&buf2, r)

	if buf1.String() != buf2.String() {
		t.Fatalf("non-deterministic output:\n%s\nvs\n%s", buf1.String(), buf2.String())
	}
}

func TestExpositionSortOrder(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("z_metric", "z help")
	r.NewCounter("a_metric", "a help")
	r.NewGauge("m_metric", "m help")

	var buf bytes.Buffer
	WriteExposition(&buf, r)
	out := buf.String()

	aIdx := strings.Index(out, "a_metric")
	mIdx := strings.Index(out, "m_metric")
	zIdx := strings.Index(out, "z_metric")

	if aIdx >= mIdx || mIdx >= zIdx {
		t.Fatalf("metrics not sorted: a=%d m=%d z=%d", aIdx, mIdx, zIdx)
	}
}
