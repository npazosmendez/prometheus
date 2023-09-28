// Copyright 2021 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package remote

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"

	"github.com/prometheus/prometheus/model/exemplar"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/prompb"
	"github.com/prometheus/prometheus/storage"
)

type writeHandler struct {
	logger     log.Logger
	appendable storage.Appendable
	// experimental feature, new remote write proto format
	internFormat bool
}

// NewWriteHandler creates a http.Handler that accepts remote write requests and
// writes them to the provided appendable.
func NewWriteHandler(logger log.Logger, appendable storage.Appendable, internFormat bool) http.Handler {
	return &writeHandler{
		logger:       logger,
		appendable:   appendable,
		internFormat: internFormat,
	}
}

func (h *writeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	var req *prompb.WriteRequest
	var reqWithRefs *prompb.WriteRequestWithRefs
	if h.internFormat {
		reqWithRefs, err = DecodeReducedWriteRequest(r.Body)
	} else {
		req, err = DecodeWriteRequest(r.Body)
	}

	if err != nil {
		level.Error(h.logger).Log("msg", "Error decoding remote write request", "err", err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if h.internFormat {
		err = h.writeReduced(r.Context(), reqWithRefs)
	} else {
		err = h.write(r.Context(), req)
	}
	switch err {
	case nil:
	case storage.ErrOutOfOrderSample, storage.ErrOutOfBounds, storage.ErrDuplicateSampleForTimestamp:
		// Indicated an out of order sample is a bad request to prevent retries.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	default:
		level.Error(h.logger).Log("msg", "Error appending remote write", "err", err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// checkAppendExemplarError modifies the AppendExamplar's returned error based on the error cause.
func (h *writeHandler) checkAppendExemplarError(err error, e *exemplar.Exemplar, outOfOrderErrs *int) error {
	unwrapedErr := errors.Unwrap(err)
	switch {
	case errors.Is(unwrapedErr, storage.ErrNotFound):
		return storage.ErrNotFound
	case errors.Is(unwrapedErr, storage.ErrOutOfOrderExemplar):
		*outOfOrderErrs++
		level.Debug(h.logger).Log("msg", "Out of order exemplar", "exemplar", fmt.Sprintf("%+v", e))
		return nil
	default:
		return err
	}
}

func (h *writeHandler) withAppender(ctx context.Context, appendAll func(storage.Appender) (outOfOrderExemplarErrs int, err error)) (err error) {
	app := h.appendable.Appender(ctx)
	defer func() {
		if err != nil {
			_ = app.Rollback()
			return
		}
		err = app.Commit()
	}()

	outOfOrderExemplarErrs, err := appendAll(app)
	if outOfOrderExemplarErrs > 0 {
		_ = level.Warn(h.logger).Log("msg", "Error on ingesting out-of-order exemplars", "num_dropped", outOfOrderExemplarErrs)
	}

	return err
}

func (h *writeHandler) write(ctx context.Context, req *prompb.WriteRequest) (err error) {
	return h.withAppender(ctx, func(app storage.Appender) (int, error) {
		outOfOrderExemplarErrs := 0
		for _, ts := range req.Timeseries {
			labels := labelProtosToLabels(ts.Labels)
			err = h.writeSamples(app, labels, ts.Samples)
			if err != nil {
				return outOfOrderExemplarErrs, err
			}

			for _, ep := range ts.Exemplars {
				e := exemplarProtoToExemplar(ep)
				h.writeExemplar(app, labels, &e, &outOfOrderExemplarErrs)
			}

			h.writeHistograms(app, labels, ts.Histograms)
			if err != nil {
				return outOfOrderExemplarErrs, err
			}
		}
		return outOfOrderExemplarErrs, nil
	})
}

func (h *writeHandler) writeReduced(ctx context.Context, req *prompb.WriteRequestWithRefs) (err error) {
	return h.withAppender(ctx, func(app storage.Appender) (int, error) {
		outOfOrderExemplarErrs := 0
		for _, ts := range req.Timeseries {
			labels := labelRefProtosToLabels(req.StringSymbolTable, ts.Labels)
			err = h.writeSamples(app, labels, ts.Samples)
			if err != nil {
				return outOfOrderExemplarErrs, err
			}

			for _, ep := range ts.Exemplars {
				e := exemplarRefProtoToExemplar(req.StringSymbolTable, ep)
				h.writeExemplar(app, labels, &e, &outOfOrderExemplarErrs)
			}

			h.writeHistograms(app, labels, ts.Histograms)
			if err != nil {
				return outOfOrderExemplarErrs, err
			}
		}
		return outOfOrderExemplarErrs, nil
	})
}

func (h *writeHandler) writeHistograms(app storage.Appender, lbls labels.Labels, hh []prompb.Histogram) error {
	for _, hp := range hh {
		hs := HistogramProtoToHistogram(hp)
		_, err := app.AppendHistogram(0, lbls, hp.Timestamp, hs)
		if err != nil {
			unwrappedErr := errors.Unwrap(err)
			// Althogh AppendHistogram does not currently return ErrDuplicateSampleForTimestamp there is
			// a note indicating its inclusion in the future.
			if errors.Is(unwrappedErr, storage.ErrOutOfOrderSample) || errors.Is(unwrappedErr, storage.ErrOutOfBounds) || errors.Is(unwrappedErr, storage.ErrDuplicateSampleForTimestamp) {
				level.Error(h.logger).Log("msg", "Out of order histogram from remote write", "err", err.Error(), "series", lbls.String(), "timestamp", hp.Timestamp)
			}
			return err
		}
	}
	return nil
}

func (h *writeHandler) writeExemplar(app storage.Appender, lbls labels.Labels, e *exemplar.Exemplar, outOfOrderExemplarErrs *int) {
	// Since exemplar storage is still experimental, we don't fail the request on ingestion errors.
	// We just log and discard any errors
	_, exemplarErr := app.AppendExemplar(0, lbls, *e)
	exemplarErr = h.checkAppendExemplarError(exemplarErr, e, outOfOrderExemplarErrs)
	if exemplarErr != nil {
		level.Debug(h.logger).Log("msg", "Error while adding exemplar in AddExemplar", "exemplar", fmt.Sprintf("%+v", e), "err", exemplarErr)
	}
}

func (h *writeHandler) writeSamples(app storage.Appender, lbls labels.Labels, samples []prompb.Sample) error {
	for _, s := range samples {
		_, err := app.Append(0, lbls, s.Timestamp, s.Value)
		if err != nil {
			unwrapedErr := errors.Unwrap(err)
			if errors.Is(unwrapedErr, storage.ErrOutOfOrderSample) || errors.Is(unwrapedErr, storage.ErrOutOfBounds) || errors.Is(unwrapedErr, storage.ErrDuplicateSampleForTimestamp) {
				level.Error(h.logger).Log("msg", "Out of order sample from remote write", "err", err.Error(), "series", lbls.String(), "timestamp", s.Timestamp)
			}
			return err
		}
	}
	return nil
}
