package qaimport

import (
	"encoding/csv"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

func readCSV(path string, header ...string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || len(rows[0]) < len(header) {
		return nil, fmt.Errorf("%s: header must be %s", path, strings.Join(header, ","))
	}
	for i, h := range header {
		if strings.TrimSpace(rows[0][i]) != h {
			return nil, fmt.Errorf("%s: header must be %s", path, strings.Join(header, ","))
		}
	}
	return rows[1:], nil
}

func LoadUsersMap(path string) (map[string]string, error) {
	rows, err := readCSV(path, "legacy_email", "canonical_email")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		out[strings.ToLower(strings.TrimSpace(r[0]))] = strings.ToLower(strings.TrimSpace(r[1]))
	}
	return out, nil
}

func LoadProjectsMap(path string) (map[uuid.UUID]string, error) {
	rows, err := readCSV(path, "legacy_project_id", "lotus_project_slug")
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID]string{}
	for i, r := range rows {
		id, err := uuid.Parse(strings.TrimSpace(r[0]))
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, i+2, err)
		}
		out[id] = strings.TrimSpace(r[1])
	}
	return out, nil
}
