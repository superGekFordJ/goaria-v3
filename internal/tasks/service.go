package tasks

import (
	"goaria-v3/internal/extractor"
)

type Service struct {
	Dispatcher ExtractorAddTaskDispatcher
	Runtime    *extractor.HostAuthRuntime
}
