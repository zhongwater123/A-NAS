package scoring

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// ExportFormat names the layout of an export; a change of meaning gets a
// new one. 2 is one table with a row per answer (docs/specs/photo-scoring.md,
// 导出); 1, three JSON Lines files, was never installed anywhere.
const ExportFormat = "a-nas-photo-scoring/2"

// Person is what the export says about a rater's account.
type Person struct {
	Username string
	Nickname string
}

// ExportOptions choose an export's format, columns and rows.
type ExportOptions struct {
	// Format is "csv" (UTF-8 with a byte order mark, for spreadsheets) or
	// "jsonl"; empty is csv.
	Format string
	// Fields choose columns beside image_id, image_path and rater, which
	// every export has: see ExportFields.
	Fields []string
	// Include adds rows beside current scores: "skips" (skips and marks),
	// "superseded" (answers a later one replaced) and "repeats" (answers to
	// photos shown again).
	Include []string
}

// Column is one column of an export, described in its manifest.
type Column struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// The columns every export has.
var requiredColumns = []Column{
	{"image_id", "图片 ID", "照片内容的 SHA-256，同一张照片在任何批次里都相同"},
	{"image_path", "路径", "照片在批次文件夹中的相对路径；同一张照片有多个副本时，CSV 中用换行分隔，JSON 中是数组"},
	{"rater", "打分人", "打分人的用户名；账号已不在时是账号 ID"},
}

// ExportFields are the choices of columns, in their order in an export.
// Each 打分项 is a field of its own between image_size and duration.
var ExportFields = []string{
	"nickname", "rater_id", "image_size",
	Overall, "composition", "light_color", Sharpness, "narrative", "preference",
	"duration", "submitted_at", "kind", "links", "viewport", "display", "pixel_ratio",
}

var fieldColumns = map[string][]Column{
	"nickname":     {{"nickname", "昵称", "打分人的昵称"}},
	"rater_id":     {{"rater_id", "账号 ID", "打分人的 A-NAS 账号 ID，改用户名也不变"}},
	"image_size":   {{"image_width", "原图宽", "按拍摄方向转正后的宽，像素"}, {"image_height", "原图高", "按拍摄方向转正后的高，像素"}},
	"duration":     {{"duration_s", "单图耗时", "这位打分人看这张照片到提交用的秒数，不含页面在后台或打开打分标准的时间；改过分的照片加上被替换那次的时间"}},
	"submitted_at": {{"submitted_at", "提交时间", "带时区的本地时间"}},
	"kind": {
		{"kind", "类型", "score 打分，skip 无法判断，flag 图片有问题"},
		{"repeat", "重复检查", "true 表示这张照片是隔一段时间再发给同一人的重复图"},
		{"superseded", "已被替换", "true 表示后来撤销重打，这条已不是当前分数"},
	},
	"links": {
		{"rating_id", "记录 ID", "这条记录在本批次中的编号"},
		{"replaces", "替换了", "撤销重打时，被这条替换的记录 ID"},
		{"repeat_of", "重复的是", "重复检查时，这张照片原来那条记录的 ID"},
	},
	"viewport":    {{"viewport_width", "打分区宽", "CSS 像素"}, {"viewport_height", "打分区高", "CSS 像素"}},
	"display":     {{"display_width", "照片显示宽", "CSS 像素"}, {"display_height", "照片显示高", "CSS 像素"}},
	"pixel_ratio": {{"pixel_ratio", "设备像素比", "屏幕的物理像素与 CSS 像素之比"}},
}

// exportIncludes are the kinds of rows besides current scores.
var exportIncludes = []string{"skips", "superseded", "repeats"}

// ExportSummary counts the rows an export has: Scores always, the others
// with their Include.
type ExportSummary struct {
	Scores, Skips, Superseded, Repeats int
}

// columns checks options against batch and returns the export's columns.
func (options *ExportOptions) columns(batch Batch) ([]Column, error) {
	switch options.Format {
	case "":
		options.Format = "csv"
	case "csv", "jsonl":
	default:
		return nil, invalid("format must be csv or jsonl")
	}
	for _, include := range options.Include {
		if !slices.Contains(exportIncludes, include) {
			return nil, invalid("rows to include are skips, superseded and repeats")
		}
	}
	columns := slices.Clone(requiredColumns)
	for _, field := range options.Fields {
		if !slices.Contains(ExportFields, field) {
			return nil, invalid("%q is not an export field", field)
		}
		if knownDimension(field) && !slices.Contains(batch.Dimensions, field) {
			return nil, invalid("this batch does not score %s", DimensionName(field))
		}
	}
	for _, field := range ExportFields {
		if !slices.Contains(options.Fields, field) {
			continue
		}
		if knownDimension(field) {
			columns = append(columns, Column{field, DimensionName(field), "0 到 5，两位小数；跳过和标记没有分数"})
			continue
		}
		columns = append(columns, fieldColumns[field]...)
	}
	return columns, nil
}

// exportRow is one answer with everything a column can show.
type exportRow struct {
	id, item, repeatOf, replaces  int64
	rater, kind, created          string
	superseded                    bool
	scores                        map[string]int
	viewMs                        int
	viewportWidth, viewportHeight sql.NullInt64
	displayWidth, displayHeight   sql.NullInt64
	pixelRatio                    sql.NullFloat64
	totalMs                       int
}

// class names what kind of row this is for ExportOptions.Include.
func (r exportRow) class() string {
	switch {
	case r.superseded:
		return "superseded"
	case r.repeatOf != 0:
		return "repeats"
	case r.kind != "score":
		return "skips"
	}
	return "scores"
}

type exportImage struct {
	sha           string
	width, height int
	paths         []string
}

// exportRows reads every answer of the batch, oldest first, with its
// scores and the time of the answers it replaced.
func exportRows(ctx context.Context, db querier, batchID string) ([]exportRow, error) {
	var rows []exportRow
	index := map[int64]int{}
	err := each(ctx, db, `SELECT id, item_id, rater, kind, score, coalesce(repeat_of, 0), coalesce(replaces, 0), superseded_at IS NOT NULL,
		view_ms, viewport_width, viewport_height, display_width, display_height, pixel_ratio, created_at
		FROM ratings WHERE batch_id = ? ORDER BY id`, []any{batchID}, func(result *sql.Rows) error {
		row := exportRow{scores: map[string]int{}}
		var overall sql.NullInt64
		if err := result.Scan(&row.id, &row.item, &row.rater, &row.kind, &overall, &row.repeatOf, &row.replaces, &row.superseded,
			&row.viewMs, &row.viewportWidth, &row.viewportHeight, &row.displayWidth, &row.displayHeight, &row.pixelRatio, &row.created); err != nil {
			return err
		}
		if overall.Valid {
			row.scores[Overall] = int(overall.Int64)
		}
		index[row.id] = len(rows)
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	err = each(ctx, db, `SELECT s.rating_id, s.dimension, s.score FROM rating_scores s JOIN ratings r ON r.id = s.rating_id
		WHERE r.batch_id = ?`, []any{batchID}, func(result *sql.Rows) error {
		var id int64
		var dimension string
		var value int
		if err := result.Scan(&id, &dimension, &value); err != nil {
			return err
		}
		if i, ok := index[id]; ok {
			rows[i].scores[dimension] = value
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// A current answer's time is the time on the photo: its own and that of
	// every answer it replaced. A replaced answer keeps its own.
	for i := range rows {
		rows[i].totalMs = rows[i].viewMs
		if rows[i].superseded {
			continue
		}
		for replaced, steps := rows[i].replaces, 0; replaced != 0 && steps < len(rows); steps++ {
			j, ok := index[replaced]
			if !ok {
				break
			}
			rows[i].totalMs += rows[j].viewMs
			replaced = rows[j].replaces
		}
	}
	return rows, nil
}

// ExportSummary counts the rows of each kind an export can have.
func (s *Store) ExportSummary(ctx context.Context, batchID string) (ExportSummary, error) {
	if _, err := s.Batch(ctx, batchID); err != nil {
		return ExportSummary{}, err
	}
	var summary ExportSummary
	err := each(ctx, s.db, `SELECT CASE WHEN superseded_at IS NOT NULL THEN 'superseded' WHEN repeat_of IS NOT NULL THEN 'repeats'
		WHEN kind <> 'score' THEN 'skips' ELSE 'scores' END AS class, count(*) FROM ratings WHERE batch_id = ? GROUP BY class`,
		[]any{batchID}, func(rows *sql.Rows) error {
			var class string
			var count int
			if err := rows.Scan(&class, &count); err != nil {
				return err
			}
			switch class {
			case "scores":
				summary.Scores = count
			case "skips":
				summary.Skips = count
			case "superseded":
				summary.Superseded = count
			case "repeats":
				summary.Repeats = count
			}
			return nil
		})
	return summary, err
}

type exportManifest struct {
	Format     string      `json:"format"`
	ExportedAt string      `json:"exportedAt"`
	Module     string      `json:"module"`
	Batch      exportBatch `json:"batch"`
	Rows       exportCount `json:"rows"`
	File       exportFile  `json:"file"`
	Columns    []Column    `json:"columns"`
}

type exportBatch struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Source     string            `json:"source"`
	Required   int               `json:"required"`
	Rubric     string            `json:"rubric"`
	Dimensions []exportDimension `json:"dimensions"`
	State      string            `json:"state"`
	CreatedAt  string            `json:"createdAt"`
}

type exportDimension struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type exportCount struct {
	// Include lists the kinds of rows besides current scores.
	Include []string `json:"include"`
	Count   int      `json:"count"`
}

type exportFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Export writes a batch as a zip of one table, a row per answer chosen by
// options, and manifest.json, which describes the batch, the rows and every
// column. people names the accounts it knows. It reads in one transaction
// on the store's only connection, so w should be a local file (StagingDir),
// not a slow client.
func (s *Store) Export(ctx context.Context, batchID string, options ExportOptions, w io.Writer, people map[string]Person, module string) error {
	batch, err := s.Batch(ctx, batchID)
	if err != nil {
		return err
	}
	columns, err := options.columns(batch)
	if err != nil {
		return err
	}
	// One read transaction, so that answers arriving meanwhile do not make
	// the rows disagree with one another.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := exportRows(ctx, tx, batchID)
	if err != nil {
		return err
	}
	images := map[int64]exportImage{}
	err = each(ctx, tx, `SELECT i.id, i.sha256, c.width, c.height,
		(SELECT group_concat(p.path, char(10)) FROM (SELECT path FROM item_paths WHERE item_id = i.id ORDER BY path) p)
		FROM items i JOIN contents c ON c.sha256 = i.sha256 WHERE i.batch_id = ?`, []any{batchID}, func(result *sql.Rows) error {
		var id int64
		var image exportImage
		var paths sql.NullString
		if err := result.Scan(&id, &image.sha, &image.width, &image.height, &paths); err != nil {
			return err
		}
		image.paths = []string{}
		if paths.Valid {
			image.paths = strings.Split(paths.String, "\n")
		}
		images[id] = image
		return nil
	})
	if err != nil {
		return err
	}

	now := s.now().UTC()
	archive := zip.NewWriter(w)
	name := "ratings." + options.Format
	entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	digest := sha256.New()
	table := newTableWriter(options.Format, io.MultiWriter(entry, digest), columns)
	if err := table.header(); err != nil {
		return err
	}
	count := 0
	for _, row := range rows {
		if class := row.class(); class != "scores" && !slices.Contains(options.Include, class) {
			continue
		}
		values := make([]any, len(columns))
		for i, column := range columns {
			values[i] = row.value(column.Key, images[row.item], people[row.rater])
		}
		if err := table.row(values); err != nil {
			return err
		}
		count++
	}
	if err := table.flush(); err != nil {
		return err
	}

	dimensions := make([]exportDimension, len(batch.Dimensions))
	for i, id := range batch.Dimensions {
		dimensions[i] = exportDimension{ID: id, Name: DimensionName(id)}
	}
	include := []string{}
	for _, kind := range exportIncludes {
		if slices.Contains(options.Include, kind) {
			include = append(include, kind)
		}
	}
	manifest, err := archive.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Deflate, Modified: now})
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(manifest)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(exportManifest{
		Format: ExportFormat, ExportedAt: now.Format(time.RFC3339), Module: module,
		Batch: exportBatch{ID: batch.ID, Name: batch.Name, Source: batch.Source, Required: batch.Required, Rubric: batch.Rubric,
			Dimensions: dimensions, State: batch.State, CreatedAt: batch.CreatedAt.UTC().Format(time.RFC3339)},
		Rows:    exportCount{Include: include, Count: count},
		File:    exportFile{Name: name, SHA256: hex.EncodeToString(digest.Sum(nil))},
		Columns: columns,
	}); err != nil {
		return err
	}
	return archive.Close()
}

// value is what column key shows for the row: a number, a string, a bool, a
// list of strings or nil for nothing.
func (r exportRow) value(key string, image exportImage, person Person) any {
	nullInt := func(value sql.NullInt64) any {
		if value.Valid && value.Int64 > 0 {
			return value.Int64
		}
		return nil
	}
	nonZero := func(value int64) any {
		if value != 0 {
			return value
		}
		return nil
	}
	switch key {
	case "image_id":
		return image.sha
	case "image_path":
		return image.paths
	case "rater":
		if person.Username != "" {
			return person.Username
		}
		return r.rater
	case "nickname":
		return person.Nickname
	case "rater_id":
		return r.rater
	case "image_width":
		return image.width
	case "image_height":
		return image.height
	case "duration_s":
		return twoPlaces(math.Round(float64(r.totalMs)/10) / 100)
	case "submitted_at":
		if at, err := time.Parse(sqlitedb.TimeLayout, r.created); err == nil {
			return at.Local().Format(time.RFC3339)
		}
		return r.created
	case "kind":
		return r.kind
	case "repeat":
		return r.repeatOf != 0
	case "superseded":
		return r.superseded
	case "rating_id":
		return r.id
	case "replaces":
		return nonZero(r.replaces)
	case "repeat_of":
		return nonZero(r.repeatOf)
	case "viewport_width":
		return nullInt(r.viewportWidth)
	case "viewport_height":
		return nullInt(r.viewportHeight)
	case "display_width":
		return nullInt(r.displayWidth)
	case "display_height":
		return nullInt(r.displayHeight)
	case "pixel_ratio":
		if r.pixelRatio.Valid && r.pixelRatio.Float64 > 0 {
			return r.pixelRatio.Float64
		}
		return nil
	}
	if score, ok := r.scores[key]; ok {
		return twoPlaces(points(score))
	}
	return nil
}

// twoPlaces is a score in points or a time in seconds, written with two
// decimals in both formats.
type twoPlaces float64

func (v twoPlaces) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatFloat(float64(v), 'f', 2, 64)), nil
}

// tableWriter writes rows as CSV or as JSON Lines with the columns' keys.
type tableWriter struct {
	format  string
	columns []Column
	w       io.Writer
	csv     *csv.Writer
}

func newTableWriter(format string, w io.Writer, columns []Column) *tableWriter {
	table := &tableWriter{format: format, columns: columns, w: w}
	if format == "csv" {
		table.csv = csv.NewWriter(w)
	}
	return table
}

func (t *tableWriter) header() error {
	if t.csv == nil {
		return nil
	}
	// The byte order mark makes spreadsheets read the file as UTF-8.
	if _, err := io.WriteString(t.w, "\xef\xbb\xbf"); err != nil {
		return err
	}
	keys := make([]string, len(t.columns))
	for i, column := range t.columns {
		keys[i] = column.Key
	}
	return t.csv.Write(keys)
}

func (t *tableWriter) row(values []any) error {
	if t.csv != nil {
		cells := make([]string, len(values))
		for i, value := range values {
			cells[i] = csvCell(value)
		}
		return t.csv.Write(cells)
	}
	// JSON objects keep the columns' order, which encoding a map would not.
	var line strings.Builder
	line.WriteByte('{')
	for i, value := range values {
		if i > 0 {
			line.WriteByte(',')
		}
		key, err := json.Marshal(t.columns[i].Key)
		if err != nil {
			return err
		}
		encoded, err := marshalValue(value)
		if err != nil {
			return err
		}
		line.Write(key)
		line.WriteByte(':')
		line.Write(encoded)
	}
	line.WriteString("}\n")
	_, err := io.WriteString(t.w, line.String())
	return err
}

func (t *tableWriter) flush() error {
	if t.csv == nil {
		return nil
	}
	t.csv.Flush()
	return t.csv.Error()
}

func marshalValue(value any) ([]byte, error) {
	var buffer strings.Builder
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(buffer.String(), "\n")), nil
}

func csvCell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case []string:
		return strings.Join(v, "\n")
	case bool:
		return strconv.FormatBool(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case twoPlaces:
		return strconv.FormatFloat(float64(v), 'f', 2, 64)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return ""
}
