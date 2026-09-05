window.BENCHMARK_DATA = {
  "lastUpdate": 1788594201218,
  "repoUrl": "https://github.com/superGekFordJ/goaria-v3",
  "entries": {
    "GoAria Core Engine Benchmarks": [
      {
        "commit": {
          "author": {
            "name": "superGekFordJ",
            "username": "superGekFordJ",
            "email": "fordjiang125@gmail.com"
          },
          "committer": {
            "name": "superGekFordJ",
            "username": "superGekFordJ",
            "email": "fordjiang125@gmail.com"
          },
          "id": "dc9f33c117d92730fda8d1aac021d69f5886eb38",
          "message": "perf(bench): migrate to b.Loop() and add comprehensive benchmark coverage\n\nReplace manual b.N loops with b.Loop() across all benchmark tests and add\nmissing allocation reporting. Remove obsolete wait benchmark and add new\nsurge store gob encoding benchmarks.",
          "timestamp": "2026-09-05T07:24:33Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/dc9f33c117d92730fda8d1aac021d69f5886eb38"
        },
        "date": 1788594198812,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 18884729,
            "unit": "ns/op\t      11 B/op\t       0 allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 18884729,
            "unit": "ns/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 11,
            "unit": "B/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 18884574,
            "unit": "ns/op\t       1 B/op\t       0 allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 18884574,
            "unit": "ns/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 1,
            "unit": "B/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 18962502,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 18962502,
            "unit": "ns/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 19101453,
            "unit": "ns/op\t       3 B/op\t       0 allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 19101453,
            "unit": "ns/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 3,
            "unit": "B/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 18955245,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 18955245,
            "unit": "ns/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39375223,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39375223,
            "unit": "ns/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39529274,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "27 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39529274,
            "unit": "ns/op",
            "extra": "27 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "27 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "27 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39572290,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39572290,
            "unit": "ns/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39599407,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39599407,
            "unit": "ns/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39366943,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39366943,
            "unit": "ns/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1198475500,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1198475500,
            "unit": "ns/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1205015700,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1205015700,
            "unit": "ns/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1206386600,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1206386600,
            "unit": "ns/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1206701800,
            "unit": "ns/op\t     112 B/op\t       1 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1206701800,
            "unit": "ns/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 112,
            "unit": "B/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1214038600,
            "unit": "ns/op\t     224 B/op\t       2 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1214038600,
            "unit": "ns/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 224,
            "unit": "B/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1219906,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "922 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1219906,
            "unit": "ns/op",
            "extra": "922 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "922 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "922 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1284875,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "952 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1284875,
            "unit": "ns/op",
            "extra": "952 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "952 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "952 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1263991,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "946 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1263991,
            "unit": "ns/op",
            "extra": "946 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "946 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "946 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1247156,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "957 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1247156,
            "unit": "ns/op",
            "extra": "957 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "957 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "957 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1248620,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "950 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1248620,
            "unit": "ns/op",
            "extra": "950 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "950 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "950 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1208669,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "969 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1208669,
            "unit": "ns/op",
            "extra": "969 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "969 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "969 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1200225,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1053 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1200225,
            "unit": "ns/op",
            "extra": "1053 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1053 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1053 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1229869,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1008 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1229869,
            "unit": "ns/op",
            "extra": "1008 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1008 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1008 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1218259,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1014 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1218259,
            "unit": "ns/op",
            "extra": "1014 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1014 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1014 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1202107,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "997 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1202107,
            "unit": "ns/op",
            "extra": "997 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "997 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "997 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6109838,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6109838,
            "unit": "ns/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6151326,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "192 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6151326,
            "unit": "ns/op",
            "extra": "192 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "192 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "192 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6094851,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6094851,
            "unit": "ns/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6180509,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6180509,
            "unit": "ns/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "194 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6016351,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "195 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6016351,
            "unit": "ns/op",
            "extra": "195 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "195 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "195 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 135.7,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8910504 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 135.7,
            "unit": "ns/op",
            "extra": "8910504 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8910504 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8910504 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 133.7,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "9134734 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 133.7,
            "unit": "ns/op",
            "extra": "9134734 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "9134734 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "9134734 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 134,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8529445 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 134,
            "unit": "ns/op",
            "extra": "8529445 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8529445 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8529445 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 136.8,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8714976 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 136.8,
            "unit": "ns/op",
            "extra": "8714976 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8714976 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8714976 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 136.2,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8451638 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 136.2,
            "unit": "ns/op",
            "extra": "8451638 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8451638 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8451638 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 558990,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "1845 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 558990,
            "unit": "ns/op",
            "extra": "1845 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "1845 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "1845 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 515411,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2233 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 515411,
            "unit": "ns/op",
            "extra": "2233 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2233 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2233 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 503625,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2085 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 503625,
            "unit": "ns/op",
            "extra": "2085 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2085 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2085 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 519513,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2364 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 519513,
            "unit": "ns/op",
            "extra": "2364 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2364 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2364 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 503832,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2410 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 503832,
            "unit": "ns/op",
            "extra": "2410 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2410 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2410 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.3,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "66384531 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.3,
            "unit": "ns/op",
            "extra": "66384531 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "66384531 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "66384531 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.44,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "62980224 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.44,
            "unit": "ns/op",
            "extra": "62980224 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "62980224 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62980224 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.34,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "66190096 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.34,
            "unit": "ns/op",
            "extra": "66190096 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "66190096 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "66190096 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.14,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "65771443 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.14,
            "unit": "ns/op",
            "extra": "65771443 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "65771443 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "65771443 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.34,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "62756253 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.34,
            "unit": "ns/op",
            "extra": "62756253 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "62756253 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "62756253 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 164.4,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7103630 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 164.4,
            "unit": "ns/op",
            "extra": "7103630 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7103630 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7103630 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 172.3,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7437625 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 172.3,
            "unit": "ns/op",
            "extra": "7437625 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7437625 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7437625 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 169,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7026937 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 169,
            "unit": "ns/op",
            "extra": "7026937 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7026937 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7026937 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 168,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7350945 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 168,
            "unit": "ns/op",
            "extra": "7350945 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7350945 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7350945 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 168.8,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "6762498 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 168.8,
            "unit": "ns/op",
            "extra": "6762498 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "6762498 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "6762498 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12344,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "105036 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12344,
            "unit": "ns/op",
            "extra": "105036 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "105036 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "105036 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12362,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "107114 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12362,
            "unit": "ns/op",
            "extra": "107114 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "107114 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "107114 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12539,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "96152 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12539,
            "unit": "ns/op",
            "extra": "96152 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "96152 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "96152 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12205,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "92552 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12205,
            "unit": "ns/op",
            "extra": "92552 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "92552 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "92552 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12234,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "90747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12234,
            "unit": "ns/op",
            "extra": "90747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "90747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "90747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 69010,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "17701 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 69010,
            "unit": "ns/op",
            "extra": "17701 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "17701 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "17701 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 67096,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "18052 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 67096,
            "unit": "ns/op",
            "extra": "18052 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "18052 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "18052 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 65346,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "18624 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 65346,
            "unit": "ns/op",
            "extra": "18624 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "18624 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "18624 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 66346,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "17712 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 66346,
            "unit": "ns/op",
            "extra": "17712 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "17712 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "17712 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 64595,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "18555 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 64595,
            "unit": "ns/op",
            "extra": "18555 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "18555 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "18555 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 38890,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "30637 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 38890,
            "unit": "ns/op",
            "extra": "30637 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "30637 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "30637 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 38416,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "31269 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 38416,
            "unit": "ns/op",
            "extra": "31269 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "31269 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "31269 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 36898,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "32755 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 36898,
            "unit": "ns/op",
            "extra": "32755 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "32755 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "32755 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 38761,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "31794 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 38761,
            "unit": "ns/op",
            "extra": "31794 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "31794 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "31794 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 37047,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "32246 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 37047,
            "unit": "ns/op",
            "extra": "32246 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "32246 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "32246 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 13851,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "85537 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 13851,
            "unit": "ns/op",
            "extra": "85537 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "85537 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "85537 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 14008,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "88480 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 14008,
            "unit": "ns/op",
            "extra": "88480 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "88480 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "88480 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 13921,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "91471 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 13921,
            "unit": "ns/op",
            "extra": "91471 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "91471 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "91471 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 14178,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "87741 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 14178,
            "unit": "ns/op",
            "extra": "87741 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "87741 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "87741 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 14085,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "83976 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 14085,
            "unit": "ns/op",
            "extra": "83976 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "83976 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "83976 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 92108,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12879 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 92108,
            "unit": "ns/op",
            "extra": "12879 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12879 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12879 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 95677,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12666 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 95677,
            "unit": "ns/op",
            "extra": "12666 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12666 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12666 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 95283,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12561 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 95283,
            "unit": "ns/op",
            "extra": "12561 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12561 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12561 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 97286,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12308 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 97286,
            "unit": "ns/op",
            "extra": "12308 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12308 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12308 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 96161,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12243 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 96161,
            "unit": "ns/op",
            "extra": "12243 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12243 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12243 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 800335,
            "unit": "ns/op\t  178108 B/op\t    3028 allocs/op",
            "extra": "1566 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 800335,
            "unit": "ns/op",
            "extra": "1566 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178108,
            "unit": "B/op",
            "extra": "1566 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1566 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 804296,
            "unit": "ns/op\t  178092 B/op\t    3028 allocs/op",
            "extra": "1502 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 804296,
            "unit": "ns/op",
            "extra": "1502 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178092,
            "unit": "B/op",
            "extra": "1502 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1502 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 804718,
            "unit": "ns/op\t  178092 B/op\t    3028 allocs/op",
            "extra": "1521 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 804718,
            "unit": "ns/op",
            "extra": "1521 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178092,
            "unit": "B/op",
            "extra": "1521 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1521 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 819466,
            "unit": "ns/op\t  178096 B/op\t    3028 allocs/op",
            "extra": "1371 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 819466,
            "unit": "ns/op",
            "extra": "1371 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178096,
            "unit": "B/op",
            "extra": "1371 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1371 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 832097,
            "unit": "ns/op\t  178095 B/op\t    3028 allocs/op",
            "extra": "1417 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 832097,
            "unit": "ns/op",
            "extra": "1417 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178095,
            "unit": "B/op",
            "extra": "1417 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1417 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1631,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "819090 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1631,
            "unit": "ns/op",
            "extra": "819090 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "819090 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "819090 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1564,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "810568 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1564,
            "unit": "ns/op",
            "extra": "810568 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "810568 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "810568 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1551,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "805644 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1551,
            "unit": "ns/op",
            "extra": "805644 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "805644 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "805644 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1564,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "784908 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1564,
            "unit": "ns/op",
            "extra": "784908 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "784908 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "784908 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1555,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "815821 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1555,
            "unit": "ns/op",
            "extra": "815821 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "815821 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "815821 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 36071300,
            "unit": "ns/op\t 1828662 B/op\t   24136 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 36071300,
            "unit": "ns/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1828662,
            "unit": "B/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24136,
            "unit": "allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 35414942,
            "unit": "ns/op\t 1820993 B/op\t   24130 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35414942,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1820993,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24130,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 35512334,
            "unit": "ns/op\t 1818883 B/op\t   24125 allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35512334,
            "unit": "ns/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818883,
            "unit": "B/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24125,
            "unit": "allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 35927974,
            "unit": "ns/op\t 1819886 B/op\t   24121 allocs/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35927974,
            "unit": "ns/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819886,
            "unit": "B/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24121,
            "unit": "allocs/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 35595859,
            "unit": "ns/op\t 1821124 B/op\t   24123 allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35595859,
            "unit": "ns/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1821124,
            "unit": "B/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24123,
            "unit": "allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 35480594,
            "unit": "ns/op\t 1818752 B/op\t   24125 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35480594,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818752,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24125,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 35961865,
            "unit": "ns/op\t 1820667 B/op\t   24127 allocs/op",
            "extra": "34 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35961865,
            "unit": "ns/op",
            "extra": "34 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1820667,
            "unit": "B/op",
            "extra": "34 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24127,
            "unit": "allocs/op",
            "extra": "34 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 35567633,
            "unit": "ns/op\t 1820674 B/op\t   24127 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35567633,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1820674,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24127,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 35442100,
            "unit": "ns/op\t 1818384 B/op\t   24126 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35442100,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818384,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24126,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 35512464,
            "unit": "ns/op\t 1819933 B/op\t   24125 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35512464,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819933,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24125,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 19996632,
            "unit": "ns/op\t 1129289 B/op\t   14236 allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 19996632,
            "unit": "ns/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1129289,
            "unit": "B/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14236,
            "unit": "allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 20427125,
            "unit": "ns/op\t 1129458 B/op\t   14232 allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20427125,
            "unit": "ns/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1129458,
            "unit": "B/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14232,
            "unit": "allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 20443846,
            "unit": "ns/op\t 1128457 B/op\t   14233 allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20443846,
            "unit": "ns/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128457,
            "unit": "B/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14233,
            "unit": "allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 20127395,
            "unit": "ns/op\t 1130813 B/op\t   14234 allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20127395,
            "unit": "ns/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1130813,
            "unit": "B/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14234,
            "unit": "allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 20227185,
            "unit": "ns/op\t 1129118 B/op\t   14235 allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20227185,
            "unit": "ns/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1129118,
            "unit": "B/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14235,
            "unit": "allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 439571,
            "unit": "ns/op\t   77879 B/op\t     960 allocs/op",
            "extra": "2722 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 439571,
            "unit": "ns/op",
            "extra": "2722 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 77879,
            "unit": "B/op",
            "extra": "2722 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2722 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 433035,
            "unit": "ns/op\t   78066 B/op\t     960 allocs/op",
            "extra": "2750 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 433035,
            "unit": "ns/op",
            "extra": "2750 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78066,
            "unit": "B/op",
            "extra": "2750 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2750 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 436496,
            "unit": "ns/op\t   77949 B/op\t     960 allocs/op",
            "extra": "2512 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 436496,
            "unit": "ns/op",
            "extra": "2512 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 77949,
            "unit": "B/op",
            "extra": "2512 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2512 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 435407,
            "unit": "ns/op\t   77997 B/op\t     960 allocs/op",
            "extra": "2700 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 435407,
            "unit": "ns/op",
            "extra": "2700 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 77997,
            "unit": "B/op",
            "extra": "2700 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2700 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 433171,
            "unit": "ns/op\t   78157 B/op\t     960 allocs/op",
            "extra": "2822 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 433171,
            "unit": "ns/op",
            "extra": "2822 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78157,
            "unit": "B/op",
            "extra": "2822 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2822 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 438190,
            "unit": "ns/op\t   78328 B/op\t     960 allocs/op",
            "extra": "2374 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 438190,
            "unit": "ns/op",
            "extra": "2374 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78328,
            "unit": "B/op",
            "extra": "2374 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2374 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 436510,
            "unit": "ns/op\t   78559 B/op\t     960 allocs/op",
            "extra": "2820 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 436510,
            "unit": "ns/op",
            "extra": "2820 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78559,
            "unit": "B/op",
            "extra": "2820 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2820 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 435297,
            "unit": "ns/op\t   78528 B/op\t     960 allocs/op",
            "extra": "2850 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 435297,
            "unit": "ns/op",
            "extra": "2850 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78528,
            "unit": "B/op",
            "extra": "2850 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2850 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 503485,
            "unit": "ns/op\t   78101 B/op\t     960 allocs/op",
            "extra": "2751 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 503485,
            "unit": "ns/op",
            "extra": "2751 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78101,
            "unit": "B/op",
            "extra": "2751 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2751 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 487220,
            "unit": "ns/op\t   78369 B/op\t     960 allocs/op",
            "extra": "2383 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 487220,
            "unit": "ns/op",
            "extra": "2383 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78369,
            "unit": "B/op",
            "extra": "2383 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2383 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1243662,
            "unit": "ns/op\t  459823 B/op\t    3384 allocs/op",
            "extra": "912 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1243662,
            "unit": "ns/op",
            "extra": "912 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 459823,
            "unit": "B/op",
            "extra": "912 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "912 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1286219,
            "unit": "ns/op\t  462464 B/op\t    3385 allocs/op",
            "extra": "873 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1286219,
            "unit": "ns/op",
            "extra": "873 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 462464,
            "unit": "B/op",
            "extra": "873 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3385,
            "unit": "allocs/op",
            "extra": "873 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1239239,
            "unit": "ns/op\t  458613 B/op\t    3383 allocs/op",
            "extra": "985 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1239239,
            "unit": "ns/op",
            "extra": "985 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 458613,
            "unit": "B/op",
            "extra": "985 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3383,
            "unit": "allocs/op",
            "extra": "985 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1261496,
            "unit": "ns/op\t  458873 B/op\t    3384 allocs/op",
            "extra": "1002 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1261496,
            "unit": "ns/op",
            "extra": "1002 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 458873,
            "unit": "B/op",
            "extra": "1002 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "1002 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1231419,
            "unit": "ns/op\t  460947 B/op\t    3384 allocs/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1231419,
            "unit": "ns/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 460947,
            "unit": "B/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 43318,
            "unit": "ns/op\t    9540 B/op\t     129 allocs/op",
            "extra": "27586 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 43318,
            "unit": "ns/op",
            "extra": "27586 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9540,
            "unit": "B/op",
            "extra": "27586 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "27586 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 42805,
            "unit": "ns/op\t    9542 B/op\t     129 allocs/op",
            "extra": "27468 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 42805,
            "unit": "ns/op",
            "extra": "27468 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9542,
            "unit": "B/op",
            "extra": "27468 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "27468 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 42681,
            "unit": "ns/op\t    9542 B/op\t     129 allocs/op",
            "extra": "28785 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 42681,
            "unit": "ns/op",
            "extra": "28785 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9542,
            "unit": "B/op",
            "extra": "28785 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "28785 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 42367,
            "unit": "ns/op\t    9545 B/op\t     129 allocs/op",
            "extra": "28122 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 42367,
            "unit": "ns/op",
            "extra": "28122 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9545,
            "unit": "B/op",
            "extra": "28122 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "28122 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 43002,
            "unit": "ns/op\t    9554 B/op\t     129 allocs/op",
            "extra": "27999 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 43002,
            "unit": "ns/op",
            "extra": "27999 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9554,
            "unit": "B/op",
            "extra": "27999 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "27999 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 118986,
            "unit": "ns/op\t  107899 B/op\t      46 allocs/op",
            "extra": "9442 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 118986,
            "unit": "ns/op",
            "extra": "9442 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107899,
            "unit": "B/op",
            "extra": "9442 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "9442 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 115145,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 115145,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 113931,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 113931,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 116527,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "9606 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 116527,
            "unit": "ns/op",
            "extra": "9606 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "9606 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "9606 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 112919,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 112919,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 147222,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8319 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 147222,
            "unit": "ns/op",
            "extra": "8319 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8319 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8319 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 150084,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8924 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 150084,
            "unit": "ns/op",
            "extra": "8924 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8924 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8924 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 146761,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "7620 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 146761,
            "unit": "ns/op",
            "extra": "7620 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "7620 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "7620 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 148122,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8697 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 148122,
            "unit": "ns/op",
            "extra": "8697 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8697 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8697 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 150239,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8264 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 150239,
            "unit": "ns/op",
            "extra": "8264 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8264 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8264 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 482457,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2485 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 482457,
            "unit": "ns/op",
            "extra": "2485 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2485 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2485 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 476266,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2678 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 476266,
            "unit": "ns/op",
            "extra": "2678 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2678 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2678 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 470581,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2610 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 470581,
            "unit": "ns/op",
            "extra": "2610 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2610 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2610 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 487034,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2481 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 487034,
            "unit": "ns/op",
            "extra": "2481 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2481 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2481 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 504906,
            "unit": "ns/op\t  486138 B/op\t      51 allocs/op",
            "extra": "2439 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 504906,
            "unit": "ns/op",
            "extra": "2439 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486138,
            "unit": "B/op",
            "extra": "2439 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2439 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 504122,
            "unit": "ns/op\t  428651 B/op\t    5431 allocs/op",
            "extra": "2490 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 504122,
            "unit": "ns/op",
            "extra": "2490 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428651,
            "unit": "B/op",
            "extra": "2490 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2490 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 515764,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "2288 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 515764,
            "unit": "ns/op",
            "extra": "2288 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "2288 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2288 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 514554,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "2326 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 514554,
            "unit": "ns/op",
            "extra": "2326 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "2326 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2326 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 515036,
            "unit": "ns/op\t  428651 B/op\t    5431 allocs/op",
            "extra": "2304 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 515036,
            "unit": "ns/op",
            "extra": "2304 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428651,
            "unit": "B/op",
            "extra": "2304 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2304 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 518150,
            "unit": "ns/op\t  428651 B/op\t    5431 allocs/op",
            "extra": "2581 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 518150,
            "unit": "ns/op",
            "extra": "2581 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428651,
            "unit": "B/op",
            "extra": "2581 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2581 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 2320185,
            "unit": "ns/op\t      40 B/op\t       1 allocs/op",
            "extra": "506 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 2320185,
            "unit": "ns/op",
            "extra": "506 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 40,
            "unit": "B/op",
            "extra": "506 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "506 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 2215471,
            "unit": "ns/op\t      29 B/op\t       1 allocs/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 2215471,
            "unit": "ns/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 29,
            "unit": "B/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 2255191,
            "unit": "ns/op\t      29 B/op\t       1 allocs/op",
            "extra": "537 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 2255191,
            "unit": "ns/op",
            "extra": "537 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 29,
            "unit": "B/op",
            "extra": "537 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "537 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 2235311,
            "unit": "ns/op\t      29 B/op\t       1 allocs/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 2235311,
            "unit": "ns/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 29,
            "unit": "B/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "540 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 2224704,
            "unit": "ns/op\t      31 B/op\t       1 allocs/op",
            "extra": "546 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 2224704,
            "unit": "ns/op",
            "extra": "546 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 31,
            "unit": "B/op",
            "extra": "546 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "546 times\n4 procs"
          }
        ]
      }
    ]
  }
}