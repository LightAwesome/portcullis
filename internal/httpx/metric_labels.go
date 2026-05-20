package httpx

import "context"

// MetricLabels is populated by inner middleware/handlers and read by
// outer metrics middleware after the request completes.
type MetricLabels struct {
	Client string
	Route  string
}

type metricLabelsCtxKey struct{}

// WithMetricLabels attaches a fresh labels holder to ctx.
func WithMetricLabels(ctx context.Context) (context.Context, *MetricLabels) {
	labels := &MetricLabels{}
	return context.WithValue(ctx, metricLabelsCtxKey{}, labels), labels
}

// MetricLabelsFromContext returns the labels holder, if present.
func MetricLabelsFromContext(ctx context.Context) *MetricLabels {
	labels, ok := ctx.Value(metricLabelsCtxKey{}).(*MetricLabels)
	if !ok {
		return nil
	}
	return labels
}
