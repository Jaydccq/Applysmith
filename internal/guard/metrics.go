package guard

// Metrics are the size figures a release report prints for a tree.
type Metrics struct {
	Files         int
	Bytes         int64
	GoLines       int
	TSLines       int
	GoDirectDeps  int
	DashboardDeps int
}

// Measure computes the release metrics of a tree.
func Measure(t *Tree) (Metrics, error) {
	var m Metrics
	m.Files = len(t.Paths)
	for _, p := range t.Paths {
		data, err := t.Read(p)
		if err != nil {
			return m, err
		}
		m.Bytes += int64(len(data))
		switch ext(p) {
		case ".go":
			m.GoLines += physicalLines(data)
		case ".ts", ".tsx":
			m.TSLines += physicalLines(data)
		}
		switch p {
		case "go.mod":
			m.GoDirectDeps = len(goDirectRequires(string(data)))
		case "web/package.json":
			n, err := npmDependencies(data)
			if err != nil {
				return m, err
			}
			m.DashboardDeps = n
		}
	}
	return m, nil
}
