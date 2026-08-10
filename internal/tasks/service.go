package tasks

import "goaria-v3/internal/rpc"

type Service struct {
	Adapter ExtractorAdapter
	Engine  rpc.DownloadEngine
}
