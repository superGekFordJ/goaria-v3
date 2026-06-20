package tasks

import (
	"goaria-v3/internal/extractor"
	"goaria-v3/internal/rpc"
)

type Service struct {
	Dispatcher ExtractorAddTaskDispatcher
	Runtime    *extractor.HostAuthRuntime
	Engine     rpc.DownloadEngine
}
