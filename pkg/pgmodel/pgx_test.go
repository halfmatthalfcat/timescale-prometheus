package pgmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v4"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/prompb"
)

type mockPGXConn struct {
	DBName            string
	ExecSQLs          []string
	ExecArgs          [][]interface{}
	ExecErr           error
	QuerySQLs         []string
	QueryArgs         [][]interface{}
	QueryResults      []interface{}
	QueryErr          error
	CopyFromTableName pgx.Identifier
	CopyFromColumns   []string
	CopyFromRowSource pgx.CopyFromSource
	CopyFromResult    int64
	CopyFromError     error
	CopyFromRowsRows  [][]interface{}
}

func (m *mockPGXConn) Close(c context.Context) error {
	return nil
}

func (m *mockPGXConn) UseDatabase(dbName string) {
	m.DBName = dbName
}

func (m *mockPGXConn) Exec(ctx context.Context, sql string, arguments ...interface{}) (pgconn.CommandTag, error) {
	m.ExecSQLs = append(m.ExecSQLs, sql)
	m.ExecArgs = append(m.ExecArgs, arguments)
	return pgconn.CommandTag([]byte{}), m.ExecErr
}

func (m *mockPGXConn) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	m.QuerySQLs = append(m.QuerySQLs, sql)
	m.QueryArgs = append(m.QueryArgs, args)
	return &mockRows{results: m.QueryResults}, m.QueryErr
}

func (m *mockPGXConn) CopyFrom(ctx context.Context, tableName pgx.Identifier, columnNames []string, rowSrc pgx.CopyFromSource) (int64, error) {
	m.CopyFromTableName = tableName
	m.CopyFromColumns = columnNames
	m.CopyFromRowSource = rowSrc
	return m.CopyFromResult, m.CopyFromError
}

func (m *mockPGXConn) CopyFromRows(rows [][]interface{}) pgx.CopyFromSource {
	m.CopyFromRowsRows = rows
	return pgx.CopyFromRows(rows)
}

type mockRows struct {
	idx     int
	results []interface{}
}

// Close closes the rows, making the connection ready for use again. It is safe
// to call Close after rows is already closed.
func (m *mockRows) Close() {
}

// Err returns any error that occurred while reading.
func (m *mockRows) Err() error {
	panic("not implemented") // TODO: Implement
}

// CommandTag returns the command tag from this query. It is only available after Rows is closed.
func (m *mockRows) CommandTag() pgconn.CommandTag {
	panic("not implemented") // TODO: Implement
}

func (m *mockRows) FieldDescriptions() []pgproto3.FieldDescription {
	panic("not implemented") // TODO: Implement
}

// Next prepares the next row for reading. It returns true if there is another
// row and false if no more rows are available. It automatically closes rows
// when all rows are read.
func (m *mockRows) Next() bool {
	return m.idx < len(m.results)
}

// Scan reads the values from the current row into dest values positionally.
// dest can include pointers to core types, values implementing the Scanner
// interface, []byte, and nil. []byte will skip the decoding process and directly
// copy the raw bytes received from PostgreSQL. nil will skip the value entirely.
func (m *mockRows) Scan(dest ...interface{}) error {
	if m.idx >= len(m.results) {
		return fmt.Errorf("scanning error")
	}

	switch m.results[m.idx].(type) {
	case uint64:
		for i := range dest {
			dv := reflect.ValueOf(dest[i])
			dvp := reflect.Indirect(dv)
			dvp.SetUint(m.results[m.idx].(uint64))
		}
	case string:
		for i := range dest {
			dv := reflect.ValueOf(dest[i])
			dvp := reflect.Indirect(dv)
			dvp.SetString(m.results[m.idx].(string))
		}
	}

	m.idx++
	return nil
}

// Values returns the decoded row values.
func (m *mockRows) Values() ([]interface{}, error) {
	panic("not implemented") // TODO: Implement
}

// RawValues returns the unparsed bytes of the row values. The returned [][]byte is only valid until the next Next
// call or the Rows is closed. However, the underlying byte data is safe to retain a reference to and mutate.
func (m *mockRows) RawValues() [][]byte {
	panic("not implemented") // TODO: Implement
}

func createLabel(name string, value string) *prompb.Label {
	return &prompb.Label{Name: name, Value: value}
}

func createLabels(x int) []*prompb.Label {
	i := 0
	labels := make([]*prompb.Label, 0, x)
	for i < x {
		labels = append(
			labels,
			createLabel(
				fmt.Sprintf("foo%d", i),
				fmt.Sprintf("bar%d", i),
			),
		)
		i++
	}

	return labels
}

func sortLabels(labels []*prompb.Label) []*prompb.Label {
	sort.Slice(labels, func(i, j int) bool {
		if labels[i].Name != labels[j].Name {
			return labels[i].Name < labels[j].Name
		}
		return labels[i].Value < labels[j].Value
	})
	return labels
}

func sortAndDedupLabels(labels []*prompb.Label) []*prompb.Label {
	if len(labels) == 0 {
		return labels
	}

	labels = sortLabels(labels)

	ret := make([]*prompb.Label, 0)
	lastName := labels[0].Name
	lastValue := labels[0].Value
	ret = append(ret, labels[0])

	for _, label := range labels {
		if (lastName == label.Name &&
			lastValue == label.Value) || label == nil {
			continue
		}
		lastName = label.Name
		lastValue = label.Value
		ret = append(ret, label)
	}

	return ret
}

func TestPGXInserterInsertLabels(t *testing.T) {
	testCases := []struct {
		name    string
		labels  []*prompb.Label
		execErr error
	}{
		{
			name:   "Zero label",
			labels: []*prompb.Label{},
		},
		{
			name:   "One label",
			labels: createLabels(1),
		},
		{
			name:   "One label",
			labels: createLabels(1),
		},
		{
			name:   "Exactly one batch",
			labels: createLabels(insertLabelBatchSize),
		},
		{
			name:   "Over one batch",
			labels: createLabels(insertLabelBatchSize + 1),
		},
		{
			name:   "Double batch",
			labels: createLabels(insertLabelBatchSize * 2),
		},
		{
			name:   "Duplicate labels",
			labels: append(createLabels(insertLabelBatchSize), createLabels(insertLabelBatchSize)...),
		},
		{
			name:    "Exec error",
			labels:  createLabels(1),
			execErr: fmt.Errorf("some error"),
		},
	}

	for _, c := range testCases {
		t.Run(c.name, func(t *testing.T) {
			mock := &mockPGXConn{
				ExecErr: c.execErr,
			}
			inserter := pgxInserter{conn: mock}

			for _, label := range c.labels {
				inserter.AddLabel(label)
			}

			labels, err := inserter.InsertLabels()

			if err != nil {
				if c.execErr == nil || err != c.execErr {
					t.Errorf("unexpected exec error:\ngot\n%s\nwanted\n%s", err, c.execErr)
				}
				return
			}

			expectedLabels := sortAndDedupLabels(c.labels)

			if c.execErr != nil {
				t.Errorf("unexpected exec error:\ngot\n%s\nwanted\n%s", err, c.execErr)
				return
			}

			if !reflect.DeepEqual(labels, expectedLabels) {
				t.Errorf("incorrect label values:\ngot\n%+v\nwanted\n%+v", labels, expectedLabels)
			}

			if len(inserter.labelsToInsert) > 0 {
				t.Errorf("labels not empty after insertion")
			}

			if len(expectedLabels) == 0 {
				return
			}

			i := 0
			size := len(labels)
			sortedLabels := sortLabels(c.labels)

			for i*insertLabelBatchSize < size {
				lastName, lastValue := "", ""
				labelsString := make([]string, 0, insertLabelBatchSize)
				start := i * insertLabelBatchSize
				end := (i + 1) * insertLabelBatchSize

				if end > size {
					end = size
				}
				for _, label := range sortedLabels[start:end] {
					if lastName == label.Name && lastValue == label.Value {
						continue
					}
					lastName = label.Name
					lastValue = label.Value
					labelsString = append(labelsString, fmt.Sprintf(labelSQLFormat, label.Name, label.Value))
				}

				sql := fmt.Sprintf(insertLabelSQL, strings.Join(labelsString, ","))

				if mock.ExecSQLs[i] != sql {
					t.Errorf("unexpected sql string:\ngot\n%s\nwanted\n%s", mock.ExecSQLs[i], sql)
				}

				i++
			}

		})
	}
}

func createSeriesFingerprints(x int) []uint64 {
	ret := make([]uint64, 0, x)
	i := 1
	x++

	for i < x {
		ret = append(ret, uint64(i))
		i++
	}

	return ret
}

func createSeriesResults(x uint64) []interface{} {
	ret := make([]interface{}, 0, x)
	var i uint64 = 1
	x++

	for i < x {
		ret = append(ret, interface{}(i))
		i++
	}

	return ret
}

func createSeries(x int) []*model.LabelSet {
	ret := make([]*model.LabelSet, 0, x)
	i := 1
	x++

	for i < x {
		var ls model.LabelSet = map[model.LabelName]model.LabelValue{
			model.LabelName(fmt.Sprintf("name_%d", i)): model.LabelValue(fmt.Sprintf("value_%d", i)),
		}
		ret = append(ret, &ls)
		i++
	}

	return ret
}

func sortAndDedupFPs(fps []uint64) []uint64 {
	if len(fps) == 0 {
		return fps
	}
	sort.Slice(fps, func(i, j int) bool { return fps[i] < fps[j] })

	ret := make([]uint64, 0, len(fps))
	lastFP := fps[0]
	ret = append(ret, fps[0])

	for _, fp := range fps {
		if lastFP == fp {
			continue
		}
		lastFP = fp
		ret = append(ret, fp)
	}

	return ret
}

func TestPGXInserterInsertSeries(t *testing.T) {
	testCases := []struct {
		name        string
		seriesFps   []uint64
		series      []*model.LabelSet
		queryResult []interface{}
		queryErr    error
	}{
		{
			name: "Zero series",
		},
		{
			name:        "One series",
			seriesFps:   createSeriesFingerprints(1),
			series:      createSeries(1),
			queryResult: createSeriesResults(1),
		},
		{
			name:        "Two series",
			seriesFps:   createSeriesFingerprints(2),
			series:      createSeries(2),
			queryResult: createSeriesResults(2),
		},
		{
			name:        "Double series",
			seriesFps:   append(createSeriesFingerprints(2), createSeriesFingerprints(1)...),
			series:      append(createSeries(2), createSeries(1)...),
			queryResult: createSeriesResults(2),
		},
		{
			name:        "Query err",
			seriesFps:   createSeriesFingerprints(2),
			series:      createSeries(2),
			queryResult: createSeriesResults(2),
			queryErr:    fmt.Errorf("some error"),
		},
	}

	for _, c := range testCases {
		t.Run(c.name, func(t *testing.T) {
			mock := &mockPGXConn{
				QueryErr:     c.queryErr,
				QueryResults: c.queryResult,
			}
			inserter := pgxInserter{conn: mock}

			for i, fp := range c.seriesFps {
				inserter.AddSeries(fp, c.series[i])
			}

			_, fps, err := inserter.InsertSeries()

			if err != nil {
				if c.queryErr == nil || err != c.queryErr {
					t.Errorf("unexpected exec error:\ngot\n%s\nwanted\n%s", err, c.queryErr)
				}
				return
			}

			if c.queryErr != nil {
				t.Errorf("expected query error:\ngot\n%s\nwanted\n%s", err, c.queryErr)
			}

			if len(inserter.seriesToInsert) > 0 {
				t.Errorf("series not empty after insertion")
			}

			if len(c.seriesFps) == 0 {
				if len(fps) != 0 {
					t.Errorf("got non-zero result: %v", fps)
				}
				return
			}
			expectedFPs := sortAndDedupFPs(c.seriesFps)

			if !reflect.DeepEqual(expectedFPs, fps) {
				t.Errorf("unexpected fingerprint result:\ngot\n%+v\nwanted\n%+v", fps, c.seriesFps)
			}

			seriesResult := make([]string, 0, len(expectedFPs))

			for i, fp := range expectedFPs {
				json, err := json.Marshal(c.series[i])

				if err != nil {
					t.Fatal("error marshaling series", err)
				}
				seriesResult = append(seriesResult, fmt.Sprintf(seriesSQLFormat, fp, json))
			}

			wantSQL := fmt.Sprintf(insertSeriesSQL, strings.Join(seriesResult, ","))

			gotSQL := mock.QuerySQLs[0]

			if wantSQL != gotSQL {
				t.Errorf("wrong query argument:\ngot\n%s\nwanted\n%s", gotSQL, wantSQL)
			}
		})
	}
}

func createRows(x int) [][]interface{} {
	ret := make([][]interface{}, 0, x)
	i := 0
	for i < x {
		ret = append(ret, []interface{}{i, i * 2, i * 3})
		i++
	}
	return ret
}

func TestPGXInserterInsertData(t *testing.T) {
	testCases := []struct {
		name           string
		rows           [][]interface{}
		copyFromResult int64
		copyFromErr    error
	}{
		{
			name: "Zero data",
		},
		{
			name: "One data",
			rows: createRows(1),
		},
		{
			name: "Two data",
			rows: createRows(2),
		},
		{
			name:        "Copy from error",
			copyFromErr: fmt.Errorf("some error"),
		},
		{
			name:           "Not all data inserted",
			rows:           createRows(5),
			copyFromResult: 4,
		},
	}
	for _, c := range testCases {
		t.Run(c.name, func(t *testing.T) {
			mock := &mockPGXConn{
				CopyFromResult: c.copyFromResult,
				CopyFromError:  c.copyFromErr,
			}
			inserter := pgxInserter{conn: mock}

			inserted, err := inserter.InsertData(c.rows)

			if err != nil {
				if c.copyFromErr != nil {
					if err != c.copyFromErr {
						t.Errorf("unexpected error:\ngot\n%s\nwanted\n%s", err, c.copyFromErr)
					}
					return
				}
				if c.copyFromResult == int64(len(c.rows)) {
					t.Errorf("unexpected error:\ngot\n%s\nwanted\nnil", err)
				}
				return
			}

			if c.copyFromErr != nil {
				t.Errorf("expected error:\ngot\nnil\nwanted\n%s", c.copyFromErr)
			}

			if inserted != uint64(c.copyFromResult) {
				t.Errorf("unexpected number of inserted rows:\ngot\n%d\nwanted\n%d", inserted, c.copyFromResult)
			}

			tName := pgx.Identifier{copyTableName}

			if !reflect.DeepEqual(mock.CopyFromTableName, tName) {
				t.Errorf("unexpected copy table:\ngot\n%s\nwanted\n%s", mock.CopyFromTableName, tName)
			}

			if !reflect.DeepEqual(mock.CopyFromColumns, copyColumns) {
				t.Errorf("unexpected columns:\ngot\n%s\nwanted\n%s", mock.CopyFromColumns, copyColumns)
			}

			if !reflect.DeepEqual(mock.CopyFromRowSource, mock.CopyFromRows(c.rows)) {
				t.Errorf("unexpected rows:\ngot\n%+v\nwanted\n%+v", mock.CopyFromRowSource, mock.CopyFromRows(c.rows))
			}
		})
	}
}
