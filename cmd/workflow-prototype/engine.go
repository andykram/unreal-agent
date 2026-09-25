package main

import "github.com/unreallabsai/unreal-agent/harness/workflow"

type Graph = workflow.Graph
type Step = workflow.Step
type Condition = workflow.Condition
type Node = workflow.Node
type State = workflow.State
type ExecutorKeyOptions = workflow.ExecutorKeyOptions

var (
	validate            = workflow.Validate
	ready               = workflow.Ready
	settle              = workflow.Settle
	advance             = workflow.Advance
	finishCheck         = workflow.FinishCheck
	submitOutput        = workflow.SubmitOutput
	assignExecutionKeys = workflow.AssignExecutionKeys
	executorKey         = workflow.ExecutorKey
	openRunStore        = workflow.OpenStore
	ErrStaleRunRevision = workflow.ErrStaleRunRevision
)
