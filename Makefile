# ── Toolchain ───────────────────────────────────────────────────────────────
GO        = go
GOFLAGS   = -trimpath -ldflags="-s -w"

# ── Targets ─────────────────────────────────────────────────────────────────
ENGINE_TARGET = bin/engine
BENCH_TARGET  = bin/bench

# ── Default ──────────────────────────────────────────────────────────────────
all: $(ENGINE_TARGET) $(BENCH_TARGET)

# ── Build rules ──────────────────────────────────────────────────────────────
bin:
	mkdir -p bin

$(ENGINE_TARGET): | bin
	@echo "[build]  compiling engine..."
	@$(GO) build $(GOFLAGS) -o $@ ./cmd/engine
	@echo "[build]  engine ready -> $@"

$(BENCH_TARGET): | bin
	@echo "[build]  compiling benchmark..."
	@$(GO) build $(GOFLAGS) -o $@ ./cmd/bench
	@echo "[build]  benchmark ready -> $@"

# ── Workflow ─────────────────────────────────────────────────────────────────
data:
	@echo "[data]   generating test data..."
	@cd data && uv run main.py
	@echo "[data]   done"

run: all data
	@echo "[engine] running engine..."
	@./$(ENGINE_TARGET)
	@echo "[bench]  running benchmark..."
	@./$(BENCH_TARGET)

test:
	@$(GO) test ./...

# ── Cleanup ──────────────────────────────────────────────────────────────────
clean:
	@echo "[clean]  removing build artifacts..."
	@rm -rf bin/* db/*
	@echo "[clean]  done"

clean-data:
	@echo "[clean]  removing test data..."
	@rm -rf data/*.csv
	@echo "[clean]  done"

# ── Phony ────────────────────────────────────────────────────────────────────
.PHONY: all data run test clean clean-data
