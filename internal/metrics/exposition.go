package metrics

import (
	"io"
	"sort"
)

// WriteExposition writes the registry's current state in Prometheus
// text exposition format to w. Output is deterministic: metrics are
// sorted by name, label sets within a metric are sorted by key.
func WriteExposition(w io.Writer, r *Registry) error {
	entries := r.Entries()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	for _, e := range entries {
		if err := writeHelpAndType(w, e); err != nil {
			return err
		}
		switch e.Type {
		case TypeCounter:
			if err := writeCounter(w, e); err != nil {
				return err
			}
		case TypeGauge:
			if err := writeGauge(w, e); err != nil {
				return err
			}
		case TypeHistogram:
			if err := writeHistogram(w, e); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeHelpAndType(w io.Writer, e *MetricEntry) error {
	if e.Help != "" {
		if _, err := io.WriteString(w, "# HELP "+e.Name+" "+e.Help+"\n"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "# TYPE "+e.Name+" "+string(e.Type)+"\n")
	return err
}

func writeCounter(w io.Writer, e *MetricEntry) error {
	if e.Counter != nil {
		_, err := io.WriteString(w, e.Name+" "+fmtUint64(e.Counter.Value())+"\n")
		return err
	}
	if e.CounterVec != nil {
		e.CounterVec.Each(func(labels []string, value uint64) {
			l := formatLabels(e.CounterVec.labels, labels)
			io.WriteString(w, e.Name+l+" "+fmtUint64(value)+"\n")
		})
	}
	return nil
}

func writeGauge(w io.Writer, e *MetricEntry) error {
	if e.Gauge != nil {
		_, err := io.WriteString(w, e.Name+" "+fmtInt64(e.Gauge.Value())+"\n")
		return err
	}
	if e.GaugeVec != nil {
		e.GaugeVec.Each(func(labels []string, value int64) {
			l := formatLabels(e.GaugeVec.labels, labels)
			io.WriteString(w, e.Name+l+" "+fmtInt64(value)+"\n")
		})
	}
	return nil
}

func writeHistogram(w io.Writer, e *MetricEntry) error {
	if e.Histogram != nil {
		return writeSingleHistogram(w, e.Name, nil, nil, e.Histogram)
	}
	if e.HistogramVec != nil {
		e.HistogramVec.Each(func(labels []string, h *Histogram) {
			writeSingleHistogram(w, e.Name, e.HistogramVec.labels, labels, h)
		})
	}
	return nil
}

func writeSingleHistogram(w io.Writer, name string, labelNames, labelValues []string, h *Histogram) error {
	counts := h.BucketCounts()
	buckets := h.Buckets()

	// Compute cumulative counts.
	var cumSum uint64
	for i := range counts {
		cumSum += counts[i]
		counts[i] = cumSum
	}

	// Write each bucket line.
	for i, boundary := range buckets {
		l := formatLabels(
			append(append([]string{}, labelNames...), "le"),
			append(append([]string{}, labelValues...), fmtFloat(boundary)),
		)
		if _, err := io.WriteString(w, name+"_bucket"+l+" "+fmtUint64(counts[i])+"\n"); err != nil {
			return err
		}
	}

	// +Inf bucket.
	l := formatLabels(
		append(append([]string{}, labelNames...), "le"),
		append(append([]string{}, labelValues...), "+Inf"),
	)
	if _, err := io.WriteString(w, name+"_bucket"+l+" "+fmtUint64(counts[len(counts)-1])+"\n"); err != nil {
		return err
	}

	// sum and count.
	if labelNames != nil {
		baseLabels := formatLabels(labelNames, labelValues)
		if _, err := io.WriteString(w, name+"_sum"+baseLabels+" "+fmtFloat(h.Sum())+"\n"); err != nil {
			return err
		}
		_, err := io.WriteString(w, name+"_count"+baseLabels+" "+fmtUint64(h.Count())+"\n")
		return err
	}
	if _, err := io.WriteString(w, name+"_sum "+fmtFloat(h.Sum())+"\n"); err != nil {
		return err
	}
	_, err := io.WriteString(w, name+"_count "+fmtUint64(h.Count())+"\n")
	return err
}
