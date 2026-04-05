package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type message struct {
	OSV     *osvEntry `json:"osv,omitempty"`
	Finding *finding  `json:"finding,omitempty"`
}

type osvEntry struct {
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

type finding struct {
	OSV          string   `json:"osv,omitempty"`
	FixedVersion string   `json:"fixed_version,omitempty"`
	Trace        []*frame `json:"trace,omitempty"`
}

type frame struct {
	Module   string    `json:"module"`
	Package  string    `json:"package,omitempty"`
	Function string    `json:"function,omitempty"`
	Receiver string    `json:"receiver,omitempty"`
	Position *position `json:"position,omitempty"`
}

type position struct {
	Filename string `json:"filename,omitempty"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

type vulnRecord struct {
	ID           string
	Summary      string
	Module       string
	Package      string
	Symbol       string
	FixedVersion string
	Location     string
}

func main() {
	os.Exit(run(os.Stdin, os.Stdout))
}

func run(in io.Reader, out io.Writer) int {
	decoder := json.NewDecoder(in)

	summaries := make(map[string]string)
	actionable := make(map[string]vulnRecord)
	noFix := make(map[string]vulnRecord)
	nonSymbolReachability := make(map[string]vulnRecord)

	for {
		var msg message
		err := decoder.Decode(&msg)
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(out, "failed to parse govulncheck JSON: %v\n", err)
			return 2
		}

		if msg.OSV != nil {
			summaries[msg.OSV.ID] = msg.OSV.Summary
		}
		if msg.Finding == nil {
			continue
		}

		record := newRecord(msg.Finding, summaries[msg.Finding.OSV])
		if record.ID == "" {
			continue
		}

		if record.Symbol == "" {
			if record.FixedVersion != "" {
				nonSymbolReachability[record.ID] = record
			}
			continue
		}

		if record.FixedVersion == "" {
			noFix[record.ID] = record
			continue
		}

		actionable[record.ID] = record
	}

	printSummary(out, actionable, noFix, nonSymbolReachability)
	if len(actionable) > 0 {
		return 1
	}
	return 0
}

func newRecord(f *finding, summary string) vulnRecord {
	record := vulnRecord{
		ID:           f.OSV,
		Summary:      summary,
		FixedVersion: f.FixedVersion,
	}

	if len(f.Trace) == 0 || f.Trace[0] == nil {
		return record
	}

	first := f.Trace[0]
	record.Module = first.Module
	record.Package = first.Package
	record.Symbol = symbolName(first)
	record.Location = positionString(first.Position)
	return record
}

func symbolName(f *frame) string {
	if f == nil {
		return ""
	}
	if f.Function == "" {
		return ""
	}
	if f.Receiver == "" {
		return f.Function
	}
	return f.Receiver + "." + f.Function
}

func positionString(pos *position) string {
	if pos == nil || pos.Line <= 0 || pos.Filename == "" {
		return ""
	}
	if pos.Column > 0 {
		return fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column)
	}
	return fmt.Sprintf("%s:%d", pos.Filename, pos.Line)
}

func printSummary(out io.Writer, actionable, noFix, nonSymbol map[string]vulnRecord) {
	if len(actionable) == 0 && len(noFix) == 0 && len(nonSymbol) == 0 {
		fmt.Fprintln(out, "govulncheck: no reachable vulnerabilities with known fixes found")
		return
	}

	if len(actionable) > 0 {
		fmt.Fprintln(out, "govulncheck: reachable vulnerabilities with known fixes:")
		printRecords(out, actionable)
	}

	if len(noFix) > 0 {
		fmt.Fprintln(out, "govulncheck: reachable vulnerabilities without a known fix were reported and left as warnings:")
		printRecords(out, noFix)
	}

	if len(nonSymbol) > 0 {
		fmt.Fprintln(out, "govulncheck: import-only or module-only findings with fixes available were reported but not treated as build failures:")
		printRecords(out, nonSymbol)
	}
}

func printRecords(out io.Writer, records map[string]vulnRecord) {
	keys := make([]string, 0, len(records))
	for id := range records {
		keys = append(keys, id)
	}
	sort.Strings(keys)

	for _, id := range keys {
		record := records[id]
		line := []string{id}
		if record.Summary != "" {
			line = append(line, record.Summary)
		}
		if record.Module != "" {
			line = append(line, "module="+record.Module)
		}
		if record.Package != "" {
			line = append(line, "package="+record.Package)
		}
		if record.Symbol != "" {
			line = append(line, "symbol="+record.Symbol)
		}
		if record.FixedVersion != "" {
			line = append(line, "fixed="+record.FixedVersion)
		}
		if record.Location != "" {
			line = append(line, "at="+record.Location)
		}
		fmt.Fprintf(out, " - %s\n", strings.Join(line, " | "))
	}
}
