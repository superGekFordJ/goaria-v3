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
      }
    ]
  }
}