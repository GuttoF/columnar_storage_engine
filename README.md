# Columnar Storage Engine

A minimal columnar storage engine written in Go. Reads trade data from CSV in parallel, stores each column as a contiguous binary file on disk, and benchmarks binary reads against CSV parsing.

This README is part docs, part essay: it explains **what** the engine does, **how** the code achieves it, and **why** the underlying techniques are fast. The intended reader is someone who already writes code but wants to build intuition about analytical storage engines before reading the source.

---

## Table of contents

1. [Why columnar storage exists](#1-why-columnar-storage-exists)
2. [OLTP vs OLAP: the workload split](#2-oltp-vs-olap-the-workload-split)
3. [The cost of CSV (and why "just parse it" is slow)](#3-the-cost-of-csv-and-why-just-parse-it-is-slow)
4. [On-disk layout](#4-on-disk-layout)
5. [Binary encoding: one type, one width, no surprises](#5-binary-encoding-one-type-one-width-no-surprises)
6. [The write path: CSV → columns](#6-the-write-path-csv--columns)
7. [Parallel parsing: chunk by newline, merge at the end](#7-parallel-parsing-chunk-by-newline-merge-at-the-end)
8. [mmap, the page cache, and why we skip `fread`](#8-mmap-the-page-cache-and-why-we-skip-fread)
9. [Zero-copy writes: `unsafe.Slice` over typed slices](#9-zero-copy-writes-unsafeslice-over-typed-slices)
10. [The read path and the benchmark](#10-the-read-path-and-the-benchmark)
11. [Edge cases and parity with the original C engine](#11-edge-cases-and-parity-with-the-original-c-engine)
12. [Benchmark numbers](#12-benchmark-numbers)
13. [Usage](#13-usage)
14. [Project structure](#14-project-structure)
15. [Further reading](#15-further-reading)
---

## 1. Why columnar storage exists

Most databases people first encounter — Postgres, MySQL, SQLite — store data **row by row**. A row is a single tuple of related values: a user, a transaction, a trade. On disk, all the fields of one row sit next to each other, and rows follow one another in sequence.

That layout is great when the query needs **whole rows**: "fetch the user with id 42 and show all their fields." A single seek + sequential read returns everything the query needs.

But many real-world queries don't want whole rows. They want a small number of columns across millions or billions of rows:

- "average trade price last quarter"
- "sum of revenue per product category"
- "count of events grouped by hour"

In a row-store, these queries still pay the I/O cost of every column they don't care about, because the disk reads pull in whole rows. If a table has 50 columns and the query touches 2, the engine still reads 25× more bytes than necessary.

**Columnar storage flips the layout.** Each column is stored as its own contiguous run of values. To compute an average over `Price`, the engine reads only the bytes of `Price.bin`. The other 49 columns are not touched. The disk reads exactly what the query needs.

Three things fall out of that flip:

1. **Less I/O** — a query that touches `k` of `N` columns reads `k/N` of the data.
2. **Better compression** — values of the same type cluster together; runs of similar values compress dramatically with simple schemes (RLE, dictionary, bit-packing, delta).
3. **SIMD-friendly scans** — a contiguous block of `float64` is exactly what vectorized CPU instructions consume in one shot.

This engine doesn't implement compression or SIMD — it's deliberately small — but it lays out data in the form that makes those optimizations possible.

---

## 2. OLTP vs OLAP: the workload split

The split between row-stores and column-stores tracks the split between two workload types:

| Property | OLTP (Online Transaction Processing) | OLAP (Online Analytical Processing) |
|---|---|---|
| Typical query | Insert/update one row, fetch one row | Aggregate millions of rows over a few columns |
| Latency target | Milliseconds per query | Seconds per query, on much more data |
| Read/write mix | Read-write balanced | Read-heavy, batch writes |
| Layout that wins | Row-store | Column-store |
| Real-world systems | Postgres, MySQL, SQLite, Oracle | ClickHouse, DuckDB, Snowflake, BigQuery, Parquet |

This engine is squarely on the OLAP side: it ingests in batch (one CSV → one columnar dataset) and is optimized for the analytical read path.

---

## 3. The cost of CSV (and why "just parse it" is slow)

CSV is the lowest common denominator of tabular data. It is also one of the most expensive formats to read at scale. The cost has nothing to do with disk and everything to do with what the CPU has to do per byte.

To read one `float64` from a CSV file, the CPU has to:

1. Find the start of the field (scan for the previous comma or line start).
2. Find the end of the field (scan forward for a comma or newline).
3. Validate that the bytes are ASCII digits, optionally with a sign, a dot, and an exponent.
4. Convert that decimal text into the binary IEEE-754 representation that the FPU actually uses.

Step 4 alone is a non-trivial algorithm: correctly rounded text-to-float parsing is hard enough that high-performance projects have published papers about it (the modern reference being Daniel Lemire's `fast_float`). Even a "fast" `strconv.ParseFloat` is dramatically slower than just reading 8 bytes from a file into a `float64`.

To read the same `float64` from a binary file, the CPU has to:

1. Read 8 bytes.

That's the entire pipeline. No scanning, no validation, no rounding. The bytes on disk are already a `float64`, in the exact memory layout the FPU expects.

The job of this engine is to pay that parsing cost **once**, when writing the columns, and never again.

---

## 4. On-disk layout

After running the engine, the `db/` directory looks like this:

```
db/
├── metadata.bin       ← table schema (column names, types, row counts)
└── columns/
    ├── Trade_ID.bin   ← raw int32 values, 4 bytes each
    ├── Symbol.bin     ← raw [64]byte rows, 64 bytes each
    ├── Price.bin      ← raw float64 values, 8 bytes each
    ├── Quantity.bin   ← raw float64 values, 8 bytes each
    └── Is_Valid.bin   ← raw uint8 values, 1 byte each
```

### `metadata.bin`

A tiny header describing the schema. Reading it gives you everything you need to interpret the column files.

```
┌──────────────────────┐
│ n_cols : uint64 LE   │  number of columns (5 here)
├──────────────────────┤
│ for each column:     │
│   name   : [64]byte  │  zero-padded UTF-8
│   type   : uint32 LE │  DataType enum (Int=0, Double=1, Bool=2, String=3)
│   n_rows : uint64 LE │  row count for this column
└──────────────────────┘
```

Per column: 64 + 4 + 8 = 76 bytes. The total file is `8 + 5 × 76 = 388 bytes`. Negligible.

### `columns/<name>.bin`

Pure raw values, no header. The byte layout is determined entirely by the column's type:

| Column | Type | Bytes/row | File size for 10M rows |
|---|---|---|---|
| `Trade_ID` | int32 | 4 | 40 MB |
| `Symbol` | [64]byte | 64 | 640 MB |
| `Price` | float64 | 8 | 80 MB |
| `Quantity` | float64 | 8 | 80 MB |
| `Is_Valid` | uint8 | 1 | 10 MB |

The total on disk (~850 MB) is dominated by the fixed-width strings. A real-world engine would dictionary-encode `Symbol` into a small `uint8` per row plus a dictionary file — turning 640 MB into roughly 10 MB. Out of scope here.

The key property: **every row of every column is the same number of bytes**. That means random access by row index is `O(1)`: to read row `i` of `Price`, seek to byte `i * 8`. No indexes, no offset tables. Fixed-width is the simplest form of columnar storage and the foundation everything else builds on.

---

## 5. Binary encoding: one type, one width, no surprises

All numeric columns use native little-endian representation, matching the CPU. On any x86_64 or ARM system, the bytes on disk are byte-for-byte identical to what's in a CPU register holding the value. That property is what makes the read path effectively free: `mmap` the file, cast the bytes to `[]float64`, done.

Strings are the awkward case. CSV strings are variable-length; columnar storage prefers fixed width. The engine picks the simplest tradeoff: every string gets exactly 64 bytes, padded with zeros after the terminator. Longer strings are truncated to 63 bytes + a null. This wastes space for short strings, but the read path stays trivial.

The metadata file lists the type explicitly. Nothing in the column file itself tells you what type it is — that's the metadata's job. This separation is intentional: the column files are pure data, no per-row overhead, no per-column header. Reading them only requires knowing their type and length, both of which live in `metadata.bin`.

---

## 6. The write path: CSV → columns

Conceptually, ingesting the CSV is one pass:

```
for each line in CSV:
    split into 5 fields
    for each field, parse into typed value
    append each typed value to its column buffer
write each column buffer to <name>.bin
write metadata.bin
```

The naive implementation has obvious bottlenecks:

- One thread does all the parsing.
- `fgets` does one syscall per line (~10 M syscalls).
- `strtok` mutates the line buffer; you pay a string-builder cost.
- `strtod` parses every number, every time.
- Each append may trigger a `realloc` that copies the buffer.
- Writing the columns at the end is a separate pass over memory.

This engine attacks each of these.

---

## 7. Parallel parsing: chunk by newline, merge at the end

The CSV is split into `runtime.NumCPU()` chunks. Each chunk is **aligned to newline boundaries** so that no line is split across two workers. The alignment is the only synchronization point — after that, the workers are fully independent.

```
file:   [header]\n[line 1]\n[line 2]\n ... [line N]\n
                  └── body ──────────────────────┘

split: [chunk 0] [chunk 1] [chunk 2] [chunk 3]
        each chunk starts after a \n, ends at a \n
```

The boundary search is done in the main goroutine with a single `bytes.IndexByte` per chunk seam — `O(N)` total across all chunks but essentially free (one scan over the file, vectorized inside the Go runtime).

Each worker parses its chunk into local typed slices: `[]int32` for `Trade_ID`, `[]byte` for `Symbol` (concatenated 64-byte rows), `[]float64` for `Price` and `Quantity`, `[]byte` for `Is_Valid`. The slices are pre-sized using a heuristic (`len(chunk) / 30` rows) to avoid most `realloc`s.

When all workers finish, the main goroutine concatenates the per-chunk slices in chunk order. This is the only "merge" step, and it's a sequence of `copy`s — purely memory-bandwidth bound, no parsing.

Error messages for skipped rows reference an **absolute row number** even though each worker only knows its **local** row index. That's done in the merge phase: walk chunks in order, sum prior chunks' line counts, and offset each chunk's errors by that sum. This keeps the hot loop branch-free of any global counter.

Why this scales: the parser is CPU-bound (text-to-number conversion is the bottleneck) and embarrassingly parallel (rows are independent). With 16 cores and a ~350 MB CSV, the parsing phase saturates roughly all of them, and total wall-clock time approaches `serial_time / cores` minus a small overhead for chunk setup and merge.

---

## 8. mmap, the page cache, and why we skip `fread`

The CSV is brought into memory with `mmap` instead of `read`/`fread`. Both reach the same kernel page cache, so the I/O cost is identical, but the user-space cost is not.

### The traditional path

`fread(buf, n, file)` does roughly:
1. Syscall into the kernel.
2. Kernel finds the relevant page in the page cache (or reads it from disk).
3. **Kernel copies the bytes from the page cache into the user-space `buf`.**
4. Returns to user-space.

That `memcpy` in step 3 is real work. For a 350 MB file read line by line, it's 350 MB of bytes copied that didn't need to move.

### The mmap path

`mmap(file)` does roughly:
1. Syscall once, returning a pointer.
2. Page faults occur lazily as the program reads bytes; the kernel maps page-cache pages directly into the process's address space.
3. **No copy.** The bytes the parser reads are the page-cache bytes.

For sequential scans over large files on a modern Linux box, this saves both the per-line syscall overhead and the per-byte copy. With `madvise(MADV_SEQUENTIAL)` the kernel also prefetches more aggressively and drops pages behind the read head, keeping the working set small.

The catch is that you give up the streaming abstraction — the whole file is "in the address space" and the parser has to treat it as a flat byte slice. For this engine, that's exactly what we want.

---

## 9. Zero-copy writes: `unsafe.Slice` over typed slices

After parsing, each column's data lives in a typed Go slice — `[]int32`, `[]float64`. To write it to disk, the engine needs a `[]byte` view of the same memory. Allocating a new buffer and copying would defeat the point.

Go's `unsafe.Slice` builds a slice header pointing at arbitrary memory:

```go
raw := unsafe.Slice((*byte)(unsafe.Pointer(unsafe.SliceData(prices))), len(prices)*8)
file.Write(raw)
```

This is safe as long as the underlying typed slice outlives the `[]byte` view (it does — both stay alive until the write returns). The resulting `Write` is a single syscall over a contiguous run of bytes, which is the cheapest possible write path.

There is one platform assumption: byte order. The `unsafe.Slice` cast preserves the native machine encoding. On any little-endian platform (x86_64, modern ARM, RISC-V), that matches the format defined by `metadata.bin`. On a big-endian platform, the bytes would be reversed. This engine targets little-endian deliberately; a portable version would byte-swap on write and read.

---

## 10. The read path and the benchmark

The benchmark answers the question: how much faster is reading a precomputed columnar binary than reparsing the source CSV?

The query is: **average `Price` over all rows.** It touches one column out of five.

### Binary read

```go
mm := mmap("db/columns/Price.bin")
prices := unsafe.Slice((*float64)(...), len(mm)/8)
sum := 0.0
for _, v := range prices { sum += v }
avg := sum / float64(len(prices))
```

There is no parsing. There are no allocations. The mmap brings the page-cache bytes into the address space; the cast reinterprets them as `float64`; the loop is a tight reduction the Go compiler turns into roughly one floating-point add per iteration (a fully vectorized version with SIMD would be faster still, but Go's compiler doesn't auto-vectorize this loop).

### CSV read

```go
mm := mmap("data/trades.csv")
for each line in mm:
    scan to third comma → field 2 (Price)
    parse text → float64
    sum += parsed
```

Same `mmap` trick, same page cache. But every row pays the cost of comma-scanning and `atof64`. Even with the custom no-exponent parser, this is dozens of cycles per row versus a single add for the binary path.

The ratio between the two times is the value proposition of columnar storage as a whole — on this dataset, ~30×. On a wider table (more columns the binary read can skip), the ratio grows linearly with the number of unused columns.

---

## 11. Edge cases and parity with the original C engine

The test-data generator deliberately writes two malformed rows at the end of the CSV:

1. **Row 9,999,999**: only 3 fields instead of 5.
2. **Row 10,000,000**: a `Symbol` padded to 1,010 chars, making the total line > 1,023 bytes.

The original C engine used a 1,024-byte stack buffer per line and skipped both: one for having the wrong field count, the other for overflowing the buffer. The Go engine keeps the same behavior on purpose:

- Lines with fewer than 5 fields produce `Row N has X fields, expected 5, skipping`.
- Lines longer than 1,023 bytes produce `Row N exceeds line buffer, skipping`.

The second check isn't a Go limitation — `mmap` happily exposes arbitrary-length lines — but matching it keeps the row counts and emitted errors identical between the two implementations. That makes side-by-side benchmarking honest: same valid rows in, same Price values written, same average computed.

---

## 12. Benchmark numbers

Single run on Linux 6.6, AMD-class x86_64, 16 cores, 10,000,000 rows.

| Operation | C baseline | Go rewrite | Speedup |
|---|---:|---:|---:|
| Engine: CSV → all 5 columns on disk | (not separately timed) | 1.71 s | — |
| Benchmark CSV: avg `Price` via reparse | 1.166 s | 0.627 s | 1.86× |
| Benchmark binary: avg `Price` via `Price.bin` | 0.036 s | 0.019 s | 1.89× |

Within the Go benchmark, the binary path is **32.5×** faster than the CSV path, a **96.9%** reduction in wall-clock time. That's the headline columnar-storage win.

---

## 13. Usage

### Requirements

- `go` ≥ 1.21 (developed on 1.25)
- [`uv`](https://github.com/astral-sh/uv) — for generating test data
- Linux or macOS (uses `syscall.Mmap`)

### Commands

```bash
make               # compile engine and benchmark
make data          # generate test CSV via Python (10M rows)
make run           # full pipeline: build → generate data → engine → benchmark
make test          # run unit tests
make clean         # remove build artifacts and db
make clean-data    # remove generated CSV files
```

### Typical first run

```bash
make
make data
make run
```

Output ends with the benchmark summary block.

---

## 14. Project structure

```
├── cmd/
│   ├── engine/main.go  # entry point — loads CSV, writes columnar binaries
│   └── bench/main.go   # binary vs CSV read benchmark
├── columnar/
│   ├── types.go        # schema, column types, sizes
│   ├── mmap.go         # mmap helper with MADV_SEQUENTIAL
│   ├── parse.go        # parallel CSV parser
│   └── save.go         # binary serializer
├── data/
│   ├── main.py         # test data generator (10M rows + 2 malformed)
│   └── trades.csv      # generated at runtime
├── db/                 # binary output (created at runtime)
│   ├── metadata.bin
│   └── columns/*.bin
└── bin/                # compiled binaries (created at runtime)
    ├── engine
    └── bench
```

---

## 15. Further reading

If this README sparked interest, the natural next steps are:

- **Apache Parquet** — the de-facto columnar file format on disk. Adds row groups, page-level compression, dictionary encoding, run-length encoding, and a footer that lets you skip whole row groups using min/max statistics. Spec: <https://parquet.apache.org/docs/file-format/>.
- **Apache Arrow** — the de-facto columnar format **in memory**. Defines a portable, language-agnostic layout for columnar data so that systems can share data without serialization. <https://arrow.apache.org/>.
- **ClickHouse architecture overview** — a production OLAP engine built entirely on columnar storage, vectorized execution, and aggressive parallelism. <https://clickhouse.com/docs/en/development/architecture>.
- **DuckDB** — a single-binary embeddable OLAP database, the SQLite of analytics. Reading the storage layer source is an excellent education in real-world columnar engineering. <https://duckdb.org/docs/internals/overview>.
