package metrics

// MetricFamily groups related metrics under a name with help text and type.
type MetricFamily struct {
	Name    string
	Help    string
	Type    string
	Metrics []Metric
}

// Metric is a single observation with optional labels and a value.
type Metric struct {
	Labels map[string]string
	Value  float64
}
