window.BENCHMARK_DATA = {
  "lastUpdate": 1792425669206,
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
          }
        ]
      },
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
          "id": "7e8644b1987f8ddc0d5b2ba9961c8e7ae0d8902d",
          "message": "refactor(monitor): decouple LiveConnections from ThreadCount to preserve allocated workers across drain events",
          "timestamp": "2026-09-09T04:39:38Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/7e8644b1987f8ddc0d5b2ba9961c8e7ae0d8902d"
        },
        "date": 1788933283213,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 18697847,
            "unit": "ns/op\t       1 B/op\t       0 allocs/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 18697847,
            "unit": "ns/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 1,
            "unit": "B/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39266633,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39266633,
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
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1196738800,
            "unit": "ns/op\t     112 B/op\t       1 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1196738800,
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
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1189744,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1003 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1189744,
            "unit": "ns/op",
            "extra": "1003 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1003 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1003 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1195472,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1195472,
            "unit": "ns/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 5837376,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 5837376,
            "unit": "ns/op",
            "extra": "202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "202 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 134.3,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8774161 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 134.3,
            "unit": "ns/op",
            "extra": "8774161 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8774161 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8774161 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 491310,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2431 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 491310,
            "unit": "ns/op",
            "extra": "2431 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2431 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2431 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.11,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "66358467 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.11,
            "unit": "ns/op",
            "extra": "66358467 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "66358467 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "66358467 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 164.2,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7341298 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 164.2,
            "unit": "ns/op",
            "extra": "7341298 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7341298 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7341298 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12204,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "100750 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12204,
            "unit": "ns/op",
            "extra": "100750 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "100750 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "100750 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 63624,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "18604 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 63624,
            "unit": "ns/op",
            "extra": "18604 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "18604 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "18604 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 36373,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "32084 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 36373,
            "unit": "ns/op",
            "extra": "32084 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "32084 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "32084 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 13382,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "85188 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 13382,
            "unit": "ns/op",
            "extra": "85188 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "85188 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "85188 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 96010,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12380 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 96010,
            "unit": "ns/op",
            "extra": "12380 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12380 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12380 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 808443,
            "unit": "ns/op\t  178094 B/op\t    3028 allocs/op",
            "extra": "1522 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 808443,
            "unit": "ns/op",
            "extra": "1522 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178094,
            "unit": "B/op",
            "extra": "1522 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1522 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1580,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "820612 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1580,
            "unit": "ns/op",
            "extra": "820612 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "820612 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "820612 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 36238185,
            "unit": "ns/op\t 1819954 B/op\t   24126 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 36238185,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819954,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24126,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 36056809,
            "unit": "ns/op\t 1818520 B/op\t   24122 allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 36056809,
            "unit": "ns/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818520,
            "unit": "B/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24122,
            "unit": "allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 20219792,
            "unit": "ns/op\t 1128251 B/op\t   14234 allocs/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20219792,
            "unit": "ns/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128251,
            "unit": "B/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14234,
            "unit": "allocs/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 431072,
            "unit": "ns/op\t   78221 B/op\t     960 allocs/op",
            "extra": "2806 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 431072,
            "unit": "ns/op",
            "extra": "2806 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78221,
            "unit": "B/op",
            "extra": "2806 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2806 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 431782,
            "unit": "ns/op\t   78804 B/op\t     960 allocs/op",
            "extra": "2871 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 431782,
            "unit": "ns/op",
            "extra": "2871 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78804,
            "unit": "B/op",
            "extra": "2871 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2871 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1219717,
            "unit": "ns/op\t  458966 B/op\t    3383 allocs/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1219717,
            "unit": "ns/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 458966,
            "unit": "B/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3383,
            "unit": "allocs/op",
            "extra": "1009 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 41101,
            "unit": "ns/op\t    9544 B/op\t     129 allocs/op",
            "extra": "29110 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 41101,
            "unit": "ns/op",
            "extra": "29110 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9544,
            "unit": "B/op",
            "extra": "29110 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "29110 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 114392,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "8758 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 114392,
            "unit": "ns/op",
            "extra": "8758 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "8758 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "8758 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 148079,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "7389 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 148079,
            "unit": "ns/op",
            "extra": "7389 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "7389 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "7389 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 481127,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2684 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 481127,
            "unit": "ns/op",
            "extra": "2684 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2684 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2684 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 560406,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "1837 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 560406,
            "unit": "ns/op",
            "extra": "1837 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "1837 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "1837 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 2401064,
            "unit": "ns/op\t      26 B/op\t       1 allocs/op",
            "extra": "529 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 2401064,
            "unit": "ns/op",
            "extra": "529 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 26,
            "unit": "B/op",
            "extra": "529 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "529 times\n4 procs"
          }
        ]
      },
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
          "id": "6edb3cc5ac1f8e591aa602a1e95f29353141ea8b",
          "message": "chore: bump version to 3.4.0",
          "timestamp": "2026-09-17T13:04:50Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/6edb3cc5ac1f8e591aa602a1e95f29353141ea8b"
        },
        "date": 1789650684985,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 11435251,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "100 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 11435251,
            "unit": "ns/op",
            "extra": "100 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "100 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "100 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 24846167,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "45 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 24846167,
            "unit": "ns/op",
            "extra": "45 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "45 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "45 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 732811100,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 732811100,
            "unit": "ns/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 776567,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1492 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 776567,
            "unit": "ns/op",
            "extra": "1492 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1492 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1492 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 778281,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1485 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 778281,
            "unit": "ns/op",
            "extra": "1485 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1485 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1485 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 3785189,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "304 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 3785189,
            "unit": "ns/op",
            "extra": "304 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "304 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "304 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 98.26,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "12433776 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 98.26,
            "unit": "ns/op",
            "extra": "12433776 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "12433776 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "12433776 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 367284,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "3327 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 367284,
            "unit": "ns/op",
            "extra": "3327 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "3327 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "3327 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 13.49,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "89913232 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 13.49,
            "unit": "ns/op",
            "extra": "89913232 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "89913232 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "89913232 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 118.6,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "10274596 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 118.6,
            "unit": "ns/op",
            "extra": "10274596 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "10274596 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "10274596 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 9597,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "122770 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 9597,
            "unit": "ns/op",
            "extra": "122770 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "122770 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "122770 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 49198,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "24068 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 49198,
            "unit": "ns/op",
            "extra": "24068 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "24068 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "24068 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 27574,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "43708 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 27574,
            "unit": "ns/op",
            "extra": "43708 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "43708 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "43708 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 10460,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "117264 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 10460,
            "unit": "ns/op",
            "extra": "117264 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "117264 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "117264 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 68126,
            "unit": "ns/op\t   19102 B/op\t     609 allocs/op",
            "extra": "17793 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 68126,
            "unit": "ns/op",
            "extra": "17793 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19102,
            "unit": "B/op",
            "extra": "17793 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "17793 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 601387,
            "unit": "ns/op\t  178093 B/op\t    3028 allocs/op",
            "extra": "2077 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 601387,
            "unit": "ns/op",
            "extra": "2077 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178093,
            "unit": "B/op",
            "extra": "2077 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "2077 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1153,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1153,
            "unit": "ns/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 21194347,
            "unit": "ns/op\t 1819620 B/op\t   24127 allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 21194347,
            "unit": "ns/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819620,
            "unit": "B/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24127,
            "unit": "allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 20883370,
            "unit": "ns/op\t 1820448 B/op\t   24126 allocs/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20883370,
            "unit": "ns/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1820448,
            "unit": "B/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24126,
            "unit": "allocs/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 12239940,
            "unit": "ns/op\t 1128550 B/op\t   14232 allocs/op",
            "extra": "88 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 12239940,
            "unit": "ns/op",
            "extra": "88 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128550,
            "unit": "B/op",
            "extra": "88 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14232,
            "unit": "allocs/op",
            "extra": "88 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 272296,
            "unit": "ns/op\t   78191 B/op\t     960 allocs/op",
            "extra": "4267 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 272296,
            "unit": "ns/op",
            "extra": "4267 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78191,
            "unit": "B/op",
            "extra": "4267 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "4267 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 271997,
            "unit": "ns/op\t   78894 B/op\t     960 allocs/op",
            "extra": "4287 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 271997,
            "unit": "ns/op",
            "extra": "4287 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78894,
            "unit": "B/op",
            "extra": "4287 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "4287 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 827014,
            "unit": "ns/op\t  457711 B/op\t    3382 allocs/op",
            "extra": "1407 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 827014,
            "unit": "ns/op",
            "extra": "1407 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 457711,
            "unit": "B/op",
            "extra": "1407 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3382,
            "unit": "allocs/op",
            "extra": "1407 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 23610,
            "unit": "ns/op\t    9540 B/op\t     129 allocs/op",
            "extra": "51030 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 23610,
            "unit": "ns/op",
            "extra": "51030 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9540,
            "unit": "B/op",
            "extra": "51030 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "51030 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 85436,
            "unit": "ns/op\t  107898 B/op\t      46 allocs/op",
            "extra": "13970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 85436,
            "unit": "ns/op",
            "extra": "13970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107898,
            "unit": "B/op",
            "extra": "13970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "13970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 105932,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 105932,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 346380,
            "unit": "ns/op\t  486136 B/op\t      51 allocs/op",
            "extra": "3549 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 346380,
            "unit": "ns/op",
            "extra": "3549 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486136,
            "unit": "B/op",
            "extra": "3549 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "3549 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 359528,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "3546 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 359528,
            "unit": "ns/op",
            "extra": "3546 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "3546 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "3546 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1385005,
            "unit": "ns/op\t      27 B/op\t       1 allocs/op",
            "extra": "879 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1385005,
            "unit": "ns/op",
            "extra": "879 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 27,
            "unit": "B/op",
            "extra": "879 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "879 times\n4 procs"
          }
        ]
      },
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
          "id": "35eeb30c46795efe29c4dadcac3b73356f2ddbb9",
          "message": "chore: bump version to 3.4.1",
          "timestamp": "2026-09-20T08:58:34Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/35eeb30c46795efe29c4dadcac3b73356f2ddbb9"
        },
        "date": 1789895129799,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 18652054,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "63 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 18652054,
            "unit": "ns/op",
            "extra": "63 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "63 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "63 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 39334447,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 39334447,
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
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1199701900,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1199701900,
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
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1194865,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "970 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1194865,
            "unit": "ns/op",
            "extra": "970 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "970 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "970 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1210703,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1020 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1210703,
            "unit": "ns/op",
            "extra": "1020 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1020 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1020 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 5732752,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "208 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 5732752,
            "unit": "ns/op",
            "extra": "208 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "208 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "208 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 128,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "9579943 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 128,
            "unit": "ns/op",
            "extra": "9579943 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "9579943 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "9579943 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 490973,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2269 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 490973,
            "unit": "ns/op",
            "extra": "2269 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2269 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2269 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.07,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "67138124 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.07,
            "unit": "ns/op",
            "extra": "67138124 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "67138124 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "67138124 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 159.7,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7296469 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 159.7,
            "unit": "ns/op",
            "extra": "7296469 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7296469 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7296469 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12404,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "88858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12404,
            "unit": "ns/op",
            "extra": "88858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "88858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "88858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 63305,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "19053 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 63305,
            "unit": "ns/op",
            "extra": "19053 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "19053 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "19053 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 35610,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "33014 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 35610,
            "unit": "ns/op",
            "extra": "33014 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "33014 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "33014 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 13251,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "82414 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 13251,
            "unit": "ns/op",
            "extra": "82414 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "82414 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "82414 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 95926,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "12578 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 95926,
            "unit": "ns/op",
            "extra": "12578 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "12578 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "12578 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 863850,
            "unit": "ns/op\t  178095 B/op\t    3028 allocs/op",
            "extra": "1438 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 863850,
            "unit": "ns/op",
            "extra": "1438 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178095,
            "unit": "B/op",
            "extra": "1438 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1438 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1676,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "802744 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1676,
            "unit": "ns/op",
            "extra": "802744 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "802744 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "802744 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 36861883,
            "unit": "ns/op\t 1818520 B/op\t   24127 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 36861883,
            "unit": "ns/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818520,
            "unit": "B/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24127,
            "unit": "allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 36772975,
            "unit": "ns/op\t 1821059 B/op\t   24130 allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 36772975,
            "unit": "ns/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1821059,
            "unit": "B/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24130,
            "unit": "allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 21122768,
            "unit": "ns/op\t 1128083 B/op\t   14231 allocs/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 21122768,
            "unit": "ns/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128083,
            "unit": "B/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14231,
            "unit": "allocs/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 433866,
            "unit": "ns/op\t   78223 B/op\t     960 allocs/op",
            "extra": "2800 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 433866,
            "unit": "ns/op",
            "extra": "2800 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78223,
            "unit": "B/op",
            "extra": "2800 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2800 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 432786,
            "unit": "ns/op\t   78382 B/op\t     960 allocs/op",
            "extra": "2342 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 432786,
            "unit": "ns/op",
            "extra": "2342 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78382,
            "unit": "B/op",
            "extra": "2342 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "2342 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1184741,
            "unit": "ns/op\t  462006 B/op\t    3384 allocs/op",
            "extra": "1046 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1184741,
            "unit": "ns/op",
            "extra": "1046 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 462006,
            "unit": "B/op",
            "extra": "1046 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "1046 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 40846,
            "unit": "ns/op\t    9543 B/op\t     129 allocs/op",
            "extra": "29079 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 40846,
            "unit": "ns/op",
            "extra": "29079 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9543,
            "unit": "B/op",
            "extra": "29079 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "29079 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 111536,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 111536,
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
            "value": 153098,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8674 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 153098,
            "unit": "ns/op",
            "extra": "8674 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8674 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8674 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 482663,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2577 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 482663,
            "unit": "ns/op",
            "extra": "2577 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2577 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2577 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 508757,
            "unit": "ns/op\t  428652 B/op\t    5431 allocs/op",
            "extra": "2392 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 508757,
            "unit": "ns/op",
            "extra": "2392 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428652,
            "unit": "B/op",
            "extra": "2392 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2392 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1737512,
            "unit": "ns/op\t      28 B/op\t       1 allocs/op",
            "extra": "688 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1737512,
            "unit": "ns/op",
            "extra": "688 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 28,
            "unit": "B/op",
            "extra": "688 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "688 times\n4 procs"
          }
        ]
      },
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
          "id": "3d92a28bbbfb064e682ea435d4b1e3e4895540e0",
          "message": "chore: bump version to 3.4.2",
          "timestamp": "2026-09-24T12:01:22Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/3d92a28bbbfb064e682ea435d4b1e3e4895540e0"
        },
        "date": 1790251705902,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 14824189,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "82 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 14824189,
            "unit": "ns/op",
            "extra": "82 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "82 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "82 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 31953488,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 31953488,
            "unit": "ns/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 955837700,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 955837700,
            "unit": "ns/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1013340,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1013340,
            "unit": "ns/op",
            "extra": "1202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1202 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1020314,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1234 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1020314,
            "unit": "ns/op",
            "extra": "1234 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1234 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1234 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 5065061,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "235 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 5065061,
            "unit": "ns/op",
            "extra": "235 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "235 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "235 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 127.9,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "9524806 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 127.9,
            "unit": "ns/op",
            "extra": "9524806 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "9524806 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "9524806 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 468724,
            "unit": "ns/op\t 1441792 B/op\t       1 allocs/op",
            "extra": "2596 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 468724,
            "unit": "ns/op",
            "extra": "2596 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1441792,
            "unit": "B/op",
            "extra": "2596 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2596 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 17.66,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "67757946 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 17.66,
            "unit": "ns/op",
            "extra": "67757946 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "67757946 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "67757946 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 155.3,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "8217050 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 155.3,
            "unit": "ns/op",
            "extra": "8217050 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "8217050 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "8217050 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 12182,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "100470 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 12182,
            "unit": "ns/op",
            "extra": "100470 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "100470 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "100470 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 64189,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "18718 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 64189,
            "unit": "ns/op",
            "extra": "18718 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "18718 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "18718 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 36711,
            "unit": "ns/op\t   68568 B/op\t      25 allocs/op",
            "extra": "32358 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 36711,
            "unit": "ns/op",
            "extra": "32358 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 68568,
            "unit": "B/op",
            "extra": "32358 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 25,
            "unit": "allocs/op",
            "extra": "32358 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 14233,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "74198 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 14233,
            "unit": "ns/op",
            "extra": "74198 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "74198 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "74198 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 85801,
            "unit": "ns/op\t   19103 B/op\t     609 allocs/op",
            "extra": "13815 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 85801,
            "unit": "ns/op",
            "extra": "13815 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 19103,
            "unit": "B/op",
            "extra": "13815 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 609,
            "unit": "allocs/op",
            "extra": "13815 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 769873,
            "unit": "ns/op\t  178094 B/op\t    3028 allocs/op",
            "extra": "1587 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 769873,
            "unit": "ns/op",
            "extra": "1587 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178094,
            "unit": "B/op",
            "extra": "1587 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1587 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1493,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "818637 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1493,
            "unit": "ns/op",
            "extra": "818637 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "818637 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "818637 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 27927765,
            "unit": "ns/op\t 1819400 B/op\t   24122 allocs/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 27927765,
            "unit": "ns/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819400,
            "unit": "B/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24122,
            "unit": "allocs/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 28177126,
            "unit": "ns/op\t 1819837 B/op\t   24121 allocs/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 28177126,
            "unit": "ns/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819837,
            "unit": "B/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24121,
            "unit": "allocs/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 16373355,
            "unit": "ns/op\t 1129840 B/op\t   14231 allocs/op",
            "extra": "74 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 16373355,
            "unit": "ns/op",
            "extra": "74 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1129840,
            "unit": "B/op",
            "extra": "74 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14231,
            "unit": "allocs/op",
            "extra": "74 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 372410,
            "unit": "ns/op\t   78191 B/op\t     960 allocs/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 372410,
            "unit": "ns/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78191,
            "unit": "B/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 368802,
            "unit": "ns/op\t   78519 B/op\t     960 allocs/op",
            "extra": "3224 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 368802,
            "unit": "ns/op",
            "extra": "3224 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78519,
            "unit": "B/op",
            "extra": "3224 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 960,
            "unit": "allocs/op",
            "extra": "3224 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1127082,
            "unit": "ns/op\t  457852 B/op\t    3382 allocs/op",
            "extra": "1078 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1127082,
            "unit": "ns/op",
            "extra": "1078 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 457852,
            "unit": "B/op",
            "extra": "1078 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3382,
            "unit": "allocs/op",
            "extra": "1078 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 31160,
            "unit": "ns/op\t    9544 B/op\t     129 allocs/op",
            "extra": "38650 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 31160,
            "unit": "ns/op",
            "extra": "38650 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9544,
            "unit": "B/op",
            "extra": "38650 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 129,
            "unit": "allocs/op",
            "extra": "38650 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 115522,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "9627 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 115522,
            "unit": "ns/op",
            "extra": "9627 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "9627 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "9627 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 138519,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "9496 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 138519,
            "unit": "ns/op",
            "extra": "9496 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "9496 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "9496 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 491597,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2668 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 491597,
            "unit": "ns/op",
            "extra": "2668 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2668 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2668 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 490166,
            "unit": "ns/op\t  428651 B/op\t    5431 allocs/op",
            "extra": "2335 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 490166,
            "unit": "ns/op",
            "extra": "2335 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428651,
            "unit": "B/op",
            "extra": "2335 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2335 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1838755,
            "unit": "ns/op\t      27 B/op\t       1 allocs/op",
            "extra": "638 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1838755,
            "unit": "ns/op",
            "extra": "638 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 27,
            "unit": "B/op",
            "extra": "638 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "638 times\n4 procs"
          }
        ]
      },
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
          "id": "f2f193c86301a51cd57721b71569be53c278a924",
          "message": "chore: bump version to 3.5.0",
          "timestamp": "2026-10-15T14:05:43Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/f2f193c86301a51cd57721b71569be53c278a924"
        },
        "date": 1792073533263,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 19541723,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "62 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 19541723,
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
            "value": 40895811,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "27 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 40895811,
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
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 1223841200,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1223841200,
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
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1167482,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1060 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1167482,
            "unit": "ns/op",
            "extra": "1060 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1060 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1060 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1172686,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1053 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1172686,
            "unit": "ns/op",
            "extra": "1053 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
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
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6267767,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "189 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6267767,
            "unit": "ns/op",
            "extra": "189 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "189 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "189 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 150.8,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8469706 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 150.8,
            "unit": "ns/op",
            "extra": "8469706 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8469706 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8469706 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 771575,
            "unit": "ns/op\t 1687556 B/op\t       1 allocs/op",
            "extra": "1525 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 771575,
            "unit": "ns/op",
            "extra": "1525 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1687556,
            "unit": "B/op",
            "extra": "1525 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "1525 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.25,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "66683707 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.25,
            "unit": "ns/op",
            "extra": "66683707 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "66683707 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "66683707 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 176.2,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "6841824 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 176.2,
            "unit": "ns/op",
            "extra": "6841824 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "6841824 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "6841824 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 29770,
            "unit": "ns/op\t   16000 B/op\t     400 allocs/op",
            "extra": "39747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 29770,
            "unit": "ns/op",
            "extra": "39747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 16000,
            "unit": "B/op",
            "extra": "39747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 400,
            "unit": "allocs/op",
            "extra": "39747 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 160846,
            "unit": "ns/op\t   80000 B/op\t    2000 allocs/op",
            "extra": "8263 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 160846,
            "unit": "ns/op",
            "extra": "8263 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 80000,
            "unit": "B/op",
            "extra": "8263 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 2000,
            "unit": "allocs/op",
            "extra": "8263 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 99649,
            "unit": "ns/op\t  114165 B/op\t     242 allocs/op",
            "extra": "12020 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 99649,
            "unit": "ns/op",
            "extra": "12020 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 114165,
            "unit": "B/op",
            "extra": "12020 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 242,
            "unit": "allocs/op",
            "extra": "12020 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 13628,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "91110 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 13628,
            "unit": "ns/op",
            "extra": "91110 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "91110 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "91110 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 109193,
            "unit": "ns/op\t   21106 B/op\t     709 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 109193,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 21106,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 709,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 807550,
            "unit": "ns/op\t  178092 B/op\t    3028 allocs/op",
            "extra": "1474 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 807550,
            "unit": "ns/op",
            "extra": "1474 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178092,
            "unit": "B/op",
            "extra": "1474 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1474 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1611,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "755634 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1611,
            "unit": "ns/op",
            "extra": "755634 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "755634 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "755634 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 35837963,
            "unit": "ns/op\t 1818736 B/op\t   24324 allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35837963,
            "unit": "ns/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818736,
            "unit": "B/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24324,
            "unit": "allocs/op",
            "extra": "30 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 35710945,
            "unit": "ns/op\t 1819919 B/op\t   24327 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 35710945,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819919,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24327,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 20142923,
            "unit": "ns/op\t 1128753 B/op\t   14333 allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20142923,
            "unit": "ns/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128753,
            "unit": "B/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14333,
            "unit": "allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 432640,
            "unit": "ns/op\t   78179 B/op\t     962 allocs/op",
            "extra": "2767 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 432640,
            "unit": "ns/op",
            "extra": "2767 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78179,
            "unit": "B/op",
            "extra": "2767 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "2767 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 433072,
            "unit": "ns/op\t   78495 B/op\t     962 allocs/op",
            "extra": "2523 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 433072,
            "unit": "ns/op",
            "extra": "2523 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78495,
            "unit": "B/op",
            "extra": "2523 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "2523 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1197920,
            "unit": "ns/op\t  462450 B/op\t    3385 allocs/op",
            "extra": "962 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1197920,
            "unit": "ns/op",
            "extra": "962 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 462450,
            "unit": "B/op",
            "extra": "962 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3385,
            "unit": "allocs/op",
            "extra": "962 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 41026,
            "unit": "ns/op\t    9539 B/op\t     130 allocs/op",
            "extra": "28767 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 41026,
            "unit": "ns/op",
            "extra": "28767 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9539,
            "unit": "B/op",
            "extra": "28767 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 130,
            "unit": "allocs/op",
            "extra": "28767 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 114191,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 114191,
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
            "value": 145975,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8497 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 145975,
            "unit": "ns/op",
            "extra": "8497 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8497 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8497 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 480887,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2653 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 480887,
            "unit": "ns/op",
            "extra": "2653 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2653 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2653 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 503275,
            "unit": "ns/op\t  428651 B/op\t    5431 allocs/op",
            "extra": "2524 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 503275,
            "unit": "ns/op",
            "extra": "2524 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428651,
            "unit": "B/op",
            "extra": "2524 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2524 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1760343,
            "unit": "ns/op\t      26 B/op\t       1 allocs/op",
            "extra": "688 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1760343,
            "unit": "ns/op",
            "extra": "688 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 26,
            "unit": "B/op",
            "extra": "688 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "688 times\n4 procs"
          }
        ]
      },
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
          "id": "926671bc2fd476fbcf0b88fd1c49cf375d60f02e",
          "message": "chore: bump version to 3.5.1",
          "timestamp": "2026-10-16T18:11:01Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/926671bc2fd476fbcf0b88fd1c49cf375d60f02e"
        },
        "date": 1792174632603,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 12466754,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 12466754,
            "unit": "ns/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 26687955,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "44 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 26687955,
            "unit": "ns/op",
            "extra": "44 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "44 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "44 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 778574500,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 778574500,
            "unit": "ns/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 792279,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1458 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 792279,
            "unit": "ns/op",
            "extra": "1458 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1458 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1458 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 773676,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1610 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 773676,
            "unit": "ns/op",
            "extra": "1610 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1610 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1610 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 4065288,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "295 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 4065288,
            "unit": "ns/op",
            "extra": "295 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "295 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "295 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 114.6,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "11055933 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 114.6,
            "unit": "ns/op",
            "extra": "11055933 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "11055933 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "11055933 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 519491,
            "unit": "ns/op\t 1687552 B/op\t       1 allocs/op",
            "extra": "2402 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 519491,
            "unit": "ns/op",
            "extra": "2402 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1687552,
            "unit": "B/op",
            "extra": "2402 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2402 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 13.62,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "87933345 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 13.62,
            "unit": "ns/op",
            "extra": "87933345 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "87933345 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "87933345 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 127.6,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "9414296 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 127.6,
            "unit": "ns/op",
            "extra": "9414296 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "9414296 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "9414296 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 10495,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "120146 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 10495,
            "unit": "ns/op",
            "extra": "120146 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "120146 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "120146 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 53597,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "22345 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 53597,
            "unit": "ns/op",
            "extra": "22345 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "22345 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "22345 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 58904,
            "unit": "ns/op\t   90359 B/op\t      37 allocs/op",
            "extra": "20265 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 58904,
            "unit": "ns/op",
            "extra": "20265 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 90359,
            "unit": "B/op",
            "extra": "20265 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 37,
            "unit": "allocs/op",
            "extra": "20265 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 11062,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "101698 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 11062,
            "unit": "ns/op",
            "extra": "101698 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "101698 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "101698 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 78422,
            "unit": "ns/op\t   21104 B/op\t     709 allocs/op",
            "extra": "15613 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 78422,
            "unit": "ns/op",
            "extra": "15613 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 21104,
            "unit": "B/op",
            "extra": "15613 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 709,
            "unit": "allocs/op",
            "extra": "15613 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 563534,
            "unit": "ns/op\t  178094 B/op\t    3028 allocs/op",
            "extra": "2163 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 563534,
            "unit": "ns/op",
            "extra": "2163 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178094,
            "unit": "B/op",
            "extra": "2163 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "2163 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1118,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1118,
            "unit": "ns/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 20916214,
            "unit": "ns/op\t 1820375 B/op\t   24326 allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20916214,
            "unit": "ns/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1820375,
            "unit": "B/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24326,
            "unit": "allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 20826975,
            "unit": "ns/op\t 1820177 B/op\t   24327 allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 20826975,
            "unit": "ns/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1820177,
            "unit": "B/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24327,
            "unit": "allocs/op",
            "extra": "57 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 12249384,
            "unit": "ns/op\t 1129080 B/op\t   14334 allocs/op",
            "extra": "93 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 12249384,
            "unit": "ns/op",
            "extra": "93 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1129080,
            "unit": "B/op",
            "extra": "93 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14334,
            "unit": "allocs/op",
            "extra": "93 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 273030,
            "unit": "ns/op\t   78119 B/op\t     962 allocs/op",
            "extra": "4335 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 273030,
            "unit": "ns/op",
            "extra": "4335 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78119,
            "unit": "B/op",
            "extra": "4335 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "4335 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 273476,
            "unit": "ns/op\t   78785 B/op\t     962 allocs/op",
            "extra": "4388 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 273476,
            "unit": "ns/op",
            "extra": "4388 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78785,
            "unit": "B/op",
            "extra": "4388 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "4388 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 843817,
            "unit": "ns/op\t  456159 B/op\t    3383 allocs/op",
            "extra": "1383 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 843817,
            "unit": "ns/op",
            "extra": "1383 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 456159,
            "unit": "B/op",
            "extra": "1383 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3383,
            "unit": "allocs/op",
            "extra": "1383 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 23879,
            "unit": "ns/op\t    9542 B/op\t     130 allocs/op",
            "extra": "49416 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 23879,
            "unit": "ns/op",
            "extra": "49416 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9542,
            "unit": "B/op",
            "extra": "49416 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 130,
            "unit": "allocs/op",
            "extra": "49416 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 90067,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "13350 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 90067,
            "unit": "ns/op",
            "extra": "13350 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "13350 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "13350 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 108804,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 108804,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 372426,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "3277 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 372426,
            "unit": "ns/op",
            "extra": "3277 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "3277 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "3277 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 361604,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "3457 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 361604,
            "unit": "ns/op",
            "extra": "3457 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "3457 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "3457 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1428911,
            "unit": "ns/op\t      26 B/op\t       1 allocs/op",
            "extra": "818 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1428911,
            "unit": "ns/op",
            "extra": "818 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 26,
            "unit": "B/op",
            "extra": "818 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "818 times\n4 procs"
          }
        ]
      },
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
          "id": "59ffd14c44cab3eaa8efe8812a95b60561c70aa2",
          "message": "chore: bump version to 3.5.2",
          "timestamp": "2026-10-17T09:57:05Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/59ffd14c44cab3eaa8efe8812a95b60561c70aa2"
        },
        "date": 1792231416041,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 19838362,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "61 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 19838362,
            "unit": "ns/op",
            "extra": "61 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "61 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "61 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 41167014,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 41167014,
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
            "value": 1331069700,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1331069700,
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
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1168470,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1058 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1168470,
            "unit": "ns/op",
            "extra": "1058 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1058 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1058 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1126429,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1087 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1126429,
            "unit": "ns/op",
            "extra": "1087 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1087 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1087 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 5625052,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "211 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 5625052,
            "unit": "ns/op",
            "extra": "211 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "211 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "211 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 146.4,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "7964214 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 146.4,
            "unit": "ns/op",
            "extra": "7964214 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "7964214 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "7964214 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 656127,
            "unit": "ns/op\t 1687553 B/op\t       1 allocs/op",
            "extra": "1753 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 656127,
            "unit": "ns/op",
            "extra": "1753 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1687553,
            "unit": "B/op",
            "extra": "1753 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "1753 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.11,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "65341682 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.11,
            "unit": "ns/op",
            "extra": "65341682 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "65341682 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "65341682 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 176.7,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "7064644 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 176.7,
            "unit": "ns/op",
            "extra": "7064644 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "7064644 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "7064644 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 13899,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "86270 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 13899,
            "unit": "ns/op",
            "extra": "86270 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "86270 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "86270 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 70940,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "16969 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 70940,
            "unit": "ns/op",
            "extra": "16969 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "16969 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "16969 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 68751,
            "unit": "ns/op\t   90359 B/op\t      37 allocs/op",
            "extra": "17199 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 68751,
            "unit": "ns/op",
            "extra": "17199 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 90359,
            "unit": "B/op",
            "extra": "17199 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 37,
            "unit": "allocs/op",
            "extra": "17199 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 12817,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "93858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 12817,
            "unit": "ns/op",
            "extra": "93858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "93858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "93858 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 110373,
            "unit": "ns/op\t   21106 B/op\t     709 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 110373,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 21106,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 709,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 799089,
            "unit": "ns/op\t  178092 B/op\t    3028 allocs/op",
            "extra": "1480 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 799089,
            "unit": "ns/op",
            "extra": "1480 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178092,
            "unit": "B/op",
            "extra": "1480 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1480 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1608,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "807884 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1608,
            "unit": "ns/op",
            "extra": "807884 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "807884 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "807884 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 34765997,
            "unit": "ns/op\t 1819477 B/op\t   24326 allocs/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 34765997,
            "unit": "ns/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819477,
            "unit": "B/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24326,
            "unit": "allocs/op",
            "extra": "31 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 34968844,
            "unit": "ns/op\t 1819282 B/op\t   24324 allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 34968844,
            "unit": "ns/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819282,
            "unit": "B/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24324,
            "unit": "allocs/op",
            "extra": "32 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 19694681,
            "unit": "ns/op\t 1128442 B/op\t   14336 allocs/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 19694681,
            "unit": "ns/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128442,
            "unit": "B/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14336,
            "unit": "allocs/op",
            "extra": "58 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 415792,
            "unit": "ns/op\t   78092 B/op\t     962 allocs/op",
            "extra": "2583 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 415792,
            "unit": "ns/op",
            "extra": "2583 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78092,
            "unit": "B/op",
            "extra": "2583 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "2583 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 414996,
            "unit": "ns/op\t   78361 B/op\t     962 allocs/op",
            "extra": "2907 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 414996,
            "unit": "ns/op",
            "extra": "2907 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78361,
            "unit": "B/op",
            "extra": "2907 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "2907 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1175254,
            "unit": "ns/op\t  458314 B/op\t    3384 allocs/op",
            "extra": "996 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1175254,
            "unit": "ns/op",
            "extra": "996 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 458314,
            "unit": "B/op",
            "extra": "996 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "996 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 39650,
            "unit": "ns/op\t    9546 B/op\t     130 allocs/op",
            "extra": "29856 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 39650,
            "unit": "ns/op",
            "extra": "29856 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9546,
            "unit": "B/op",
            "extra": "29856 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 130,
            "unit": "allocs/op",
            "extra": "29856 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 109781,
            "unit": "ns/op\t  107899 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 109781,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107899,
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
            "value": 141548,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "8367 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 141548,
            "unit": "ns/op",
            "extra": "8367 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "8367 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "8367 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 453277,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2780 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 453277,
            "unit": "ns/op",
            "extra": "2780 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2780 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2780 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 473242,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "2473 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 473242,
            "unit": "ns/op",
            "extra": "2473 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "2473 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2473 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1739407,
            "unit": "ns/op\t      34 B/op\t       1 allocs/op",
            "extra": "668 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1739407,
            "unit": "ns/op",
            "extra": "668 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 34,
            "unit": "B/op",
            "extra": "668 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "668 times\n4 procs"
          }
        ]
      },
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
          "id": "29a3f1c6e07bb93759ac68ecb6f2579223bbaae7",
          "message": "chore: bump version to 3.5.3",
          "timestamp": "2026-10-19T08:31:34Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/29a3f1c6e07bb93759ac68ecb6f2579223bbaae7"
        },
        "date": 1792399084059,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 19758445,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 19758445,
            "unit": "ns/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "56 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 40845246,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "28 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 40845246,
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
            "value": 1234647900,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "1 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 1234647900,
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
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 1208435,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1038 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1208435,
            "unit": "ns/op",
            "extra": "1038 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1038 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1038 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 1180465,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1035 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 1180465,
            "unit": "ns/op",
            "extra": "1035 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1035 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1035 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 6206902,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "193 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 6206902,
            "unit": "ns/op",
            "extra": "193 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "193 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "193 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 149.1,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "8136872 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 149.1,
            "unit": "ns/op",
            "extra": "8136872 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "8136872 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "8136872 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 693248,
            "unit": "ns/op\t 1687552 B/op\t       1 allocs/op",
            "extra": "1806 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 693248,
            "unit": "ns/op",
            "extra": "1806 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1687552,
            "unit": "B/op",
            "extra": "1806 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "1806 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 18.21,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "65680725 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 18.21,
            "unit": "ns/op",
            "extra": "65680725 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "65680725 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "65680725 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 181.2,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "6453753 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 181.2,
            "unit": "ns/op",
            "extra": "6453753 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "6453753 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "6453753 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 14209,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "87398 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 14209,
            "unit": "ns/op",
            "extra": "87398 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "87398 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "87398 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 72592,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "16796 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 72592,
            "unit": "ns/op",
            "extra": "16796 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "16796 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "16796 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 72735,
            "unit": "ns/op\t   90359 B/op\t      37 allocs/op",
            "extra": "16311 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 72735,
            "unit": "ns/op",
            "extra": "16311 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 90359,
            "unit": "B/op",
            "extra": "16311 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 37,
            "unit": "allocs/op",
            "extra": "16311 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 13523,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "85512 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 13523,
            "unit": "ns/op",
            "extra": "85512 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "85512 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "85512 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 112815,
            "unit": "ns/op\t   21106 B/op\t     709 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 112815,
            "unit": "ns/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 21106,
            "unit": "B/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 709,
            "unit": "allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 813463,
            "unit": "ns/op\t  178096 B/op\t    3028 allocs/op",
            "extra": "1533 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 813463,
            "unit": "ns/op",
            "extra": "1533 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178096,
            "unit": "B/op",
            "extra": "1533 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "1533 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1588,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "821157 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1588,
            "unit": "ns/op",
            "extra": "821157 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "821157 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "821157 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 33987742,
            "unit": "ns/op\t 1818182 B/op\t   24322 allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 33987742,
            "unit": "ns/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1818182,
            "unit": "B/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24322,
            "unit": "allocs/op",
            "extra": "33 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 33961250,
            "unit": "ns/op\t 1821148 B/op\t   24330 allocs/op",
            "extra": "36 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 33961250,
            "unit": "ns/op",
            "extra": "36 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1821148,
            "unit": "B/op",
            "extra": "36 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24330,
            "unit": "allocs/op",
            "extra": "36 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 19589940,
            "unit": "ns/op\t 1128796 B/op\t   14333 allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 19589940,
            "unit": "ns/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128796,
            "unit": "B/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14333,
            "unit": "allocs/op",
            "extra": "60 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 436089,
            "unit": "ns/op\t   77666 B/op\t     962 allocs/op",
            "extra": "2630 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 436089,
            "unit": "ns/op",
            "extra": "2630 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 77666,
            "unit": "B/op",
            "extra": "2630 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "2630 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 444843,
            "unit": "ns/op\t   78621 B/op\t     962 allocs/op",
            "extra": "2421 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 444843,
            "unit": "ns/op",
            "extra": "2421 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78621,
            "unit": "B/op",
            "extra": "2421 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "2421 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 1268128,
            "unit": "ns/op\t  459932 B/op\t    3384 allocs/op",
            "extra": "926 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 1268128,
            "unit": "ns/op",
            "extra": "926 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 459932,
            "unit": "B/op",
            "extra": "926 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "926 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 45009,
            "unit": "ns/op\t    9538 B/op\t     130 allocs/op",
            "extra": "24619 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 45009,
            "unit": "ns/op",
            "extra": "24619 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9538,
            "unit": "B/op",
            "extra": "24619 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 130,
            "unit": "allocs/op",
            "extra": "24619 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 116204,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "10000 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 116204,
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
            "value": 145295,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "7834 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 145295,
            "unit": "ns/op",
            "extra": "7834 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "7834 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "7834 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 480397,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "2298 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 480397,
            "unit": "ns/op",
            "extra": "2298 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "2298 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "2298 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 488366,
            "unit": "ns/op\t  428650 B/op\t    5431 allocs/op",
            "extra": "2552 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 488366,
            "unit": "ns/op",
            "extra": "2552 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428650,
            "unit": "B/op",
            "extra": "2552 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "2552 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1714990,
            "unit": "ns/op\t      28 B/op\t       1 allocs/op",
            "extra": "704 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1714990,
            "unit": "ns/op",
            "extra": "704 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 28,
            "unit": "B/op",
            "extra": "704 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "704 times\n4 procs"
          }
        ]
      },
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
          "id": "ef729b927c11c1086379e98ba17896901112dca3",
          "message": "chore: bump version to 3.5.4",
          "timestamp": "2026-10-19T15:54:44Z",
          "url": "https://github.com/superGekFordJ/goaria-v3/commit/ef729b927c11c1086379e98ba17896901112dca3"
        },
        "date": 1792425664938,
        "tool": "go",
        "benches": [
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history)",
            "value": 12644167,
            "unit": "ns/op\t       1 B/op\t       0 allocs/op",
            "extra": "94 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 12644167,
            "unit": "ns/op",
            "extra": "94 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 1,
            "unit": "B/op",
            "extra": "94 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "94 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 27224974,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 27224974,
            "unit": "ns/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "43 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 782758900,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 782758900,
            "unit": "ns/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveBatchCurrent/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "2 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history)",
            "value": 811172,
            "unit": "ns/op\t  879113 B/op\t      72 allocs/op",
            "extra": "1444 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 811172,
            "unit": "ns/op",
            "extra": "1444 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879113,
            "unit": "B/op",
            "extra": "1444 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1444 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history)",
            "value": 783750,
            "unit": "ns/op\t  879112 B/op\t      72 allocs/op",
            "extra": "1538 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - ns/op",
            "value": 783750,
            "unit": "ns/op",
            "extra": "1538 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - B/op",
            "value": 879112,
            "unit": "B/op",
            "extra": "1538 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/FrontHeavy100From10000 (goaria-v3/internal/history) - allocs/op",
            "value": 72,
            "unit": "allocs/op",
            "extra": "1538 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history)",
            "value": 4376563,
            "unit": "ns/op\t 3566000 B/op\t     266 allocs/op",
            "extra": "271 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - ns/op",
            "value": 4376563,
            "unit": "ns/op",
            "extra": "271 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - B/op",
            "value": 3566000,
            "unit": "B/op",
            "extra": "271 times\n4 procs"
          },
          {
            "name": "BenchmarkRemoveManyBatch/Spread1000From50000 (goaria-v3/internal/history) - allocs/op",
            "value": 266,
            "unit": "allocs/op",
            "extra": "271 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history)",
            "value": 113.8,
            "unit": "ns/op\t      16 B/op\t       2 allocs/op",
            "extra": "10406467 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - ns/op",
            "value": 113.8,
            "unit": "ns/op",
            "extra": "10406467 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - B/op",
            "value": 16,
            "unit": "B/op",
            "extra": "10406467 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_Update (goaria-v3/internal/history) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "10406467 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history)",
            "value": 544932,
            "unit": "ns/op\t 1687552 B/op\t       1 allocs/op",
            "extra": "2263 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - ns/op",
            "value": 544932,
            "unit": "ns/op",
            "extra": "2263 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - B/op",
            "value": 1687552,
            "unit": "B/op",
            "extra": "2263 times\n4 procs"
          },
          {
            "name": "BenchmarkGetAll_Scan (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "2263 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history)",
            "value": 13.64,
            "unit": "ns/op\t       0 B/op\t       0 allocs/op",
            "extra": "88674099 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - ns/op",
            "value": 13.64,
            "unit": "ns/op",
            "extra": "88674099 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - B/op",
            "value": 0,
            "unit": "B/op",
            "extra": "88674099 times\n4 procs"
          },
          {
            "name": "BenchmarkContainsSource (goaria-v3/internal/history) - allocs/op",
            "value": 0,
            "unit": "allocs/op",
            "extra": "88674099 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history)",
            "value": 130.3,
            "unit": "ns/op\t      22 B/op\t       1 allocs/op",
            "extra": "9541574 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - ns/op",
            "value": 130.3,
            "unit": "ns/op",
            "extra": "9541574 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - B/op",
            "value": 22,
            "unit": "B/op",
            "extra": "9541574 times\n4 procs"
          },
          {
            "name": "BenchmarkAdd_New (goaria-v3/internal/history) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "9541574 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor)",
            "value": 10727,
            "unit": "ns/op\t    8000 B/op\t     200 allocs/op",
            "extra": "102990 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 10727,
            "unit": "ns/op",
            "extra": "102990 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - B/op",
            "value": 8000,
            "unit": "B/op",
            "extra": "102990 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 200,
            "unit": "allocs/op",
            "extra": "102990 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor)",
            "value": 53958,
            "unit": "ns/op\t   40000 B/op\t    1000 allocs/op",
            "extra": "22149 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - ns/op",
            "value": 53958,
            "unit": "ns/op",
            "extra": "22149 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - B/op",
            "value": 40000,
            "unit": "B/op",
            "extra": "22149 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_EnrichTasks_500 (goaria-v3/internal/monitor) - allocs/op",
            "value": 1000,
            "unit": "allocs/op",
            "extra": "22149 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor)",
            "value": 59818,
            "unit": "ns/op\t   90359 B/op\t      37 allocs/op",
            "extra": "20570 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 59818,
            "unit": "ns/op",
            "extra": "20570 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - B/op",
            "value": 90359,
            "unit": "B/op",
            "extra": "20570 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_UpdateFromAria2_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 37,
            "unit": "allocs/op",
            "extra": "20570 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor)",
            "value": 11070,
            "unit": "ns/op\t   28672 B/op\t       4 allocs/op",
            "extra": "109250 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - ns/op",
            "value": 11070,
            "unit": "ns/op",
            "extra": "109250 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - B/op",
            "value": 28672,
            "unit": "B/op",
            "extra": "109250 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskCache_GetLiveTaskLists (goaria-v3/internal/monitor) - allocs/op",
            "value": 4,
            "unit": "allocs/op",
            "extra": "109250 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor)",
            "value": 78793,
            "unit": "ns/op\t   21104 B/op\t     709 allocs/op",
            "extra": "15334 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - ns/op",
            "value": 78793,
            "unit": "ns/op",
            "extra": "15334 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - B/op",
            "value": 21104,
            "unit": "B/op",
            "extra": "15334 times\n4 procs"
          },
          {
            "name": "BenchmarkTaskTracker_Update_100 (goaria-v3/internal/monitor) - allocs/op",
            "value": 709,
            "unit": "allocs/op",
            "extra": "15334 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc)",
            "value": 584142,
            "unit": "ns/op\t  178094 B/op\t    3028 allocs/op",
            "extra": "2109 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 584142,
            "unit": "ns/op",
            "extra": "2109 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - B/op",
            "value": 178094,
            "unit": "B/op",
            "extra": "2109 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/HeavyPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 3028,
            "unit": "allocs/op",
            "extra": "2109 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc)",
            "value": 1148,
            "unit": "ns/op\t     200 B/op\t       2 allocs/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - ns/op",
            "value": 1148,
            "unit": "ns/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - B/op",
            "value": 200,
            "unit": "B/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkUnmarshalTasks/LightPayload (goaria-v3/internal/rpc) - allocs/op",
            "value": 2,
            "unit": "allocs/op",
            "extra": "1000000 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc)",
            "value": 21200839,
            "unit": "ns/op\t 1819013 B/op\t   24322 allocs/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 21200839,
            "unit": "ns/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819013,
            "unit": "B/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24322,
            "unit": "allocs/op",
            "extra": "54 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc)",
            "value": 22622712,
            "unit": "ns/op\t 1819756 B/op\t   24327 allocs/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 22622712,
            "unit": "ns/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1819756,
            "unit": "B/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 24327,
            "unit": "allocs/op",
            "extra": "52 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc)",
            "value": 12583120,
            "unit": "ns/op\t 1128210 B/op\t   14332 allocs/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - ns/op",
            "value": 12583120,
            "unit": "ns/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - B/op",
            "value": 1128210,
            "unit": "B/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Sequential (goaria-v3/internal/rpc) - allocs/op",
            "value": 14332,
            "unit": "allocs/op",
            "extra": "97 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc)",
            "value": 284608,
            "unit": "ns/op\t   78337 B/op\t     962 allocs/op",
            "extra": "4280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 284608,
            "unit": "ns/op",
            "extra": "4280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78337,
            "unit": "B/op",
            "extra": "4280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchPause_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "4280 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc)",
            "value": 283161,
            "unit": "ns/op\t   78663 B/op\t     962 allocs/op",
            "extra": "4264 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 283161,
            "unit": "ns/op",
            "extra": "4264 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 78663,
            "unit": "B/op",
            "extra": "4264 times\n4 procs"
          },
          {
            "name": "BenchmarkBatchResume_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 962,
            "unit": "allocs/op",
            "extra": "4264 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc)",
            "value": 886038,
            "unit": "ns/op\t  460906 B/op\t    3384 allocs/op",
            "extra": "1348 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - ns/op",
            "value": 886038,
            "unit": "ns/op",
            "extra": "1348 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - B/op",
            "value": 460906,
            "unit": "B/op",
            "extra": "1348 times\n4 procs"
          },
          {
            "name": "BenchmarkGetTaskMetadata_Multi (goaria-v3/internal/rpc) - allocs/op",
            "value": 3384,
            "unit": "allocs/op",
            "extra": "1348 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc)",
            "value": 25005,
            "unit": "ns/op\t    9541 B/op\t     130 allocs/op",
            "extra": "47835 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - ns/op",
            "value": 25005,
            "unit": "ns/op",
            "extra": "47835 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - B/op",
            "value": 9541,
            "unit": "B/op",
            "extra": "47835 times\n4 procs"
          },
          {
            "name": "BenchmarkGetGlobalStat (goaria-v3/internal/rpc) - allocs/op",
            "value": 130,
            "unit": "allocs/op",
            "extra": "47835 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store)",
            "value": 93981,
            "unit": "ns/op\t  107896 B/op\t      46 allocs/op",
            "extra": "12970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 93981,
            "unit": "ns/op",
            "extra": "12970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 107896,
            "unit": "B/op",
            "extra": "12970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 46,
            "unit": "allocs/op",
            "extra": "12970 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store)",
            "value": 121662,
            "unit": "ns/op\t   98536 B/op\t    1431 allocs/op",
            "extra": "9676 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - ns/op",
            "value": 121662,
            "unit": "ns/op",
            "extra": "9676 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - B/op",
            "value": 98536,
            "unit": "B/op",
            "extra": "9676 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_100 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 1431,
            "unit": "allocs/op",
            "extra": "9676 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store)",
            "value": 360589,
            "unit": "ns/op\t  486137 B/op\t      51 allocs/op",
            "extra": "3116 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 360589,
            "unit": "ns/op",
            "extra": "3116 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 486137,
            "unit": "B/op",
            "extra": "3116 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Encode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 51,
            "unit": "allocs/op",
            "extra": "3116 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store)",
            "value": 361922,
            "unit": "ns/op\t  428651 B/op\t    5431 allocs/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - ns/op",
            "value": 361922,
            "unit": "ns/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - B/op",
            "value": 428651,
            "unit": "B/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkGobMasterState_Decode_500 (goaria-v3/internal/surge/store) - allocs/op",
            "value": 5431,
            "unit": "allocs/op",
            "extra": "3280 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp)",
            "value": 1424887,
            "unit": "ns/op\t      31 B/op\t       1 allocs/op",
            "extra": "842 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - ns/op",
            "value": 1424887,
            "unit": "ns/op",
            "extra": "842 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - B/op",
            "value": 31,
            "unit": "B/op",
            "extra": "842 times\n4 procs"
          },
          {
            "name": "BenchmarkWindowReclaim_TrimWorkingSet (goaria-v3/internal/wailsapp) - allocs/op",
            "value": 1,
            "unit": "allocs/op",
            "extra": "842 times\n4 procs"
          }
        ]
      }
    ]
  }
}