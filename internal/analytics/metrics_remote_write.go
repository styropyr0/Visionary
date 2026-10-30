package analytics

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	sdk "github.com/styropyr0/visionary/sdks/go"
)

// Prometheus remote_write (version 1): snappy-compressed WriteRequest
// protobufs, read with protowire so the Prometheus module is not linked in.
// Series ending in _total, or typed counter by the metadata Prometheus sends,
// are counters; _bucket series with an le label (with _sum and _count) are
// histograms; the rest are gauges. The job label becomes the service and
// instance the instance.

const remoteWriteMaxDecoded = 32 << 20

func (s *Server) handleRemoteWrite(w http.ResponseWriter, r *http.Request) {
	if !s.ingestKeyValid(logIngestKey(r), "server") {
		writeError(w, http.StatusUnauthorized, "missing or invalid ingest key")
		return
	}
	if ct := r.Header.Get("Content-Type"); strings.Contains(ct, "io.prometheus.write.v2") {
		writeError(w, http.StatusUnsupportedMediaType, "only remote_write 1.0 (prometheus.WriteRequest) is supported")
		return
	}
	enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if enc != "snappy" && enc != "" && enc != "identity" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Encoding must be snappy")
		return
	}
	o := s.otlp()
	select {
	case o.inflight <- struct{}{}:
		defer func() { <-o.inflight }()
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "too many concurrent requests")
		return
	}
	syncLimit(o.limiter, o.s.confInt("otlp_rate"))
	if !o.limiter.Allow() {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "rate exceeded")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, otlpMaxBody))
	if err != nil {
		writeError(w, otlpReadStatus(err), "the body could not be read")
		return
	}
	if enc == "snappy" {
		if body, err = snappyDecode(body, remoteWriteMaxDecoded); err != nil {
			writeError(w, http.StatusBadRequest, "the snappy body is invalid")
			return
		}
	}
	series, meta, err := parseWriteRequest(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the body is not a valid remote_write request")
		return
	}
	mi := s.metricIngest()
	mi.noteTypes(meta)
	rows := &otlpRows{}
	for _, p := range mi.promPoints(series) {
		mi.add(rows, p)
	}
	if err := o.write(r.Context(), rows); err != nil {
		s.logger.Warn("remote_write", "err", err)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "the database is unavailable; retry later")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type promSample struct {
	v  float64
	ms int64
}

type promSeries struct {
	labels  map[string]string
	samples []promSample
}

var errRemoteWrite = errors.New("bad remote_write request")

// parseWriteRequest reads the series and the metadata (family -> type).
func parseWriteRequest(b []byte) ([]promSeries, map[string]string, error) {
	var out []promSeries
	meta := map[string]string{}
	err := protoFields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
		switch {
		case num == 1 && typ == protowire.BytesType:
			ts := promSeries{labels: map[string]string{}}
			err := protoFields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
				switch {
				case num == 1 && typ == protowire.BytesType:
					var name, value string
					err := protoFields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) error {
						if typ == protowire.BytesType && num == 1 {
							name = string(v)
						} else if typ == protowire.BytesType && num == 2 {
							value = string(v)
						}
						return nil
					})
					if err != nil {
						return err
					}
					ts.labels[name] = value
				case num == 2 && typ == protowire.BytesType:
					var sm promSample
					err := protoFields(v, func(num protowire.Number, typ protowire.Type, _ []byte, n uint64) error {
						if num == 1 && typ == protowire.Fixed64Type {
							sm.v = math.Float64frombits(n)
						} else if num == 2 && typ == protowire.VarintType {
							sm.ms = int64(n)
						}
						return nil
					})
					if err != nil {
						return err
					}
					ts.samples = append(ts.samples, sm)
				}
				return nil
			})
			if err != nil {
				return err
			}
			out = append(out, ts)
		case num == 3 && typ == protowire.BytesType:
			var kind uint64
			var family string
			err := protoFields(v, func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error {
				if num == 1 && typ == protowire.VarintType {
					kind = n
				} else if num == 2 && typ == protowire.BytesType {
					family = string(v)
				}
				return nil
			})
			if err != nil {
				return err
			}
			if t := promTypeNames[kind]; t != "" && family != "" {
				meta[family] = t
			}
		}
		return nil
	})
	return out, meta, err
}

// promTypeNames are prometheus.MetricMetadata.MetricType values.
var promTypeNames = map[uint64]string{1: "counter", 2: "gauge", 3: "histogram", 4: "gaugehistogram", 5: "summary", 6: "info", 7: "stateset"}

// protoFields calls f for each field of a message: bytes fields get v,
// varint and fixed fields get n.
func protoFields(b []byte, f func(num protowire.Number, typ protowire.Type, v []byte, n uint64) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return errRemoteWrite
		}
		b = b[n:]
		var v []byte
		var x uint64
		switch typ {
		case protowire.BytesType:
			v, n = protowire.ConsumeBytes(b)
		case protowire.VarintType:
			x, n = protowire.ConsumeVarint(b)
		case protowire.Fixed64Type:
			x, n = protowire.ConsumeFixed64(b)
		case protowire.Fixed32Type:
			var y uint32
			y, n = protowire.ConsumeFixed32(b)
			x = uint64(y)
		default:
			n = protowire.ConsumeFieldValue(num, typ, b)
		}
		if n < 0 {
			return errRemoteWrite
		}
		b = b[n:]
		if err := f(num, typ, v, x); err != nil {
			return err
		}
	}
	return nil
}

func (mi *metricIngest) noteTypes(meta map[string]string) {
	if len(meta) == 0 {
		return
	}
	mi.mu.Lock()
	defer mi.mu.Unlock()
	if len(mi.promTypes) > maxMetricNames {
		mi.promTypes = map[string]string{}
	}
	for k, v := range meta {
		mi.promTypes[k] = v
	}
}

func (mi *metricIngest) promType(family string) string {
	mi.mu.Lock()
	defer mi.mu.Unlock()
	return mi.promTypes[family]
}

// promPoints turns series into readings, assembling classic histograms from
// their _bucket, _sum and _count series at the same timestamp.
func (mi *metricIngest) promPoints(series []promSeries) []metricPoint {
	type histKey struct {
		family, labels string
		ms             int64
	}
	type hist struct {
		p        metricPoint
		les      map[float64]float64
		sum, cnt float64
		hasSum   bool
		hasCnt   bool
	}
	hists := map[histKey]*hist{}
	var order []histKey
	base := func(labels map[string]string, ms int64) (metricPoint, string) {
		rest := make(map[string]string, len(labels))
		for k, v := range labels {
			if k != "__name__" && k != "job" && k != "instance" {
				rest[k] = v
			}
		}
		return metricPoint{at: time.UnixMilli(ms), service: labels["job"], instance: labels["instance"], labels: rest}, labels["__name__"]
	}
	histOf := func(family string, labels map[string]string, ms int64, create bool) *hist {
		p, _ := base(labels, ms)
		delete(p.labels, "le")
		lt, _ := canonicalLabels(p.labels)
		k := histKey{family, p.service + "\n" + p.instance + "\n" + lt, ms}
		h := hists[k]
		if h == nil && create {
			p.name, p.kind, p.cumulative = family, "histogram", true
			h = &hist{p: p, les: map[float64]float64{}}
			hists[k] = h
			order = append(order, k)
		}
		return h
	}
	// Buckets first, so _sum and _count know which families are histograms.
	for _, ts := range series {
		name := ts.labels["__name__"]
		family, isBucket := strings.CutSuffix(name, "_bucket")
		le, hasLe := ts.labels["le"]
		if !isBucket || !hasLe {
			continue
		}
		bound, err := strconv.ParseFloat(le, 64)
		if err != nil {
			continue
		}
		for _, sm := range ts.samples {
			if !math.IsNaN(sm.v) {
				histOf(family, ts.labels, sm.ms, true).les[bound] = sm.v
			}
		}
	}
	var out []metricPoint
	for _, ts := range series {
		name := ts.labels["__name__"]
		if name == "" || strings.HasSuffix(name, "_created") {
			continue
		}
		if _, hasLe := ts.labels["le"]; hasLe && strings.HasSuffix(name, "_bucket") {
			continue
		}
		for _, sm := range ts.samples {
			if math.IsNaN(sm.v) || math.IsInf(sm.v, 0) {
				continue
			}
			if family, ok := strings.CutSuffix(name, "_sum"); ok {
				if h := histOf(family, ts.labels, sm.ms, false); h != nil {
					h.sum, h.hasSum = sm.v, true
					continue
				}
			}
			if family, ok := strings.CutSuffix(name, "_count"); ok {
				if h := histOf(family, ts.labels, sm.ms, false); h != nil {
					h.cnt, h.hasCnt = sm.v, true
					continue
				}
			}
			p, _ := base(ts.labels, sm.ms)
			p.name, p.value, p.kind = name, sm.v, "gauge"
			if mi.promCounter(name) {
				p.kind, p.cumulative = "counter", true
			}
			out = append(out, p)
		}
	}
	for _, k := range order {
		h := hists[k]
		bounds := make([]float64, 0, len(h.les))
		for b := range h.les {
			if !math.IsInf(b, 1) {
				bounds = append(bounds, b)
			}
		}
		sort.Float64s(bounds)
		total := h.cnt
		if inf, ok := h.les[math.Inf(1)]; ok && !h.hasCnt {
			total = inf
		}
		hp := &histPoint{count: total, sum: h.sum, min: math.NaN(), max: math.NaN(), bounds: bounds, counts: make([]float64, len(bounds)+1)}
		prev := 0.0
		for i, b := range bounds {
			hp.counts[i] = math.Max(0, h.les[b]-prev)
			prev = h.les[b]
		}
		hp.counts[len(bounds)] = math.Max(0, total-prev)
		h.p.hist = hp
		out = append(out, h.p)
	}
	return out
}

// promCounter: typed counter by metadata, or named like one.
func (mi *metricIngest) promCounter(name string) bool {
	if t := mi.promType(name); t != "" {
		return t == "counter"
	}
	family := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_total"), "_sum"), "_count")
	switch mi.promType(family) {
	case "counter", "summary", "histogram":
		return true
	case "":
		return strings.HasSuffix(name, "_total")
	}
	return false
}

// canonicalLabels is the contract's labels text and series id with an empty name.
func canonicalLabels(labels map[string]string) (string, string) {
	return sdk.MetricSeries("", labels)
}

var errSnappy = errors.New("snappy: corrupt input")

// snappyDecode decodes the snappy block format (not the framed stream
// format), which remote_write uses.
func snappyDecode(src []byte, maxLen int) ([]byte, error) {
	n, k := binary.Uvarint(src)
	if k <= 0 || n > uint64(maxLen) {
		return nil, errSnappy
	}
	src = src[k:]
	dst := make([]byte, 0, n)
	for len(src) > 0 {
		tag := src[0]
		switch tag & 3 {
		case 0:
			length := int(tag >> 2)
			src = src[1:]
			if length >= 60 {
				extra := length - 59
				if len(src) < extra {
					return nil, errSnappy
				}
				length = 0
				for i := extra - 1; i >= 0; i-- {
					length = length<<8 | int(src[i])
				}
				src = src[extra:]
			}
			length++
			if length <= 0 || len(src) < length || len(dst)+length > int(n) {
				return nil, errSnappy
			}
			dst = append(dst, src[:length]...)
			src = src[length:]
			continue
		case 1:
			if len(src) < 2 {
				return nil, errSnappy
			}
			length := 4 + int(tag>>2)&7
			offset := int(tag&0xe0)<<3 | int(src[1])
			src = src[2:]
			if dst = snappyCopy(dst, offset, length, int(n)); dst == nil {
				return nil, errSnappy
			}
		case 2:
			if len(src) < 3 {
				return nil, errSnappy
			}
			length := 1 + int(tag>>2)
			offset := int(binary.LittleEndian.Uint16(src[1:3]))
			src = src[3:]
			if dst = snappyCopy(dst, offset, length, int(n)); dst == nil {
				return nil, errSnappy
			}
		case 3:
			if len(src) < 5 {
				return nil, errSnappy
			}
			length := 1 + int(tag>>2)
			offset := int(binary.LittleEndian.Uint32(src[1:5]))
			src = src[5:]
			if dst = snappyCopy(dst, offset, length, int(n)); dst == nil {
				return nil, errSnappy
			}
		}
	}
	if len(dst) != int(n) {
		return nil, errSnappy
	}
	return dst, nil
}

// snappyCopy appends length bytes from offset back; copies may overlap.
func snappyCopy(dst []byte, offset, length, limit int) []byte {
	if offset <= 0 || offset > len(dst) || len(dst)+length > limit {
		return nil
	}
	start := len(dst) - offset
	for i := 0; i < length; i++ {
		dst = append(dst, dst[start+i])
	}
	return dst
}
