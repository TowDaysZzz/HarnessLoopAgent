package mysqlstore

import (
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/dailyreview"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memoryworkflow"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

var (
	_ auth.Repository                 = (*Store)(nil)
	_ chat.Repository                 = (*Store)(nil)
	_ knowledgebase.Repository        = (*Store)(nil)
	_ memory.Repository               = (*Store)(nil)
	_ memory.MutationVersionReader    = (*Store)(nil)
	_ memory.ContextRefSource         = (*Store)(nil)
	_ dailyreview.CacheRepository     = (*Store)(nil)
	_ memoryworkflow.EditPayloadStore = (*Store)(nil)
	_ note.Repository                 = (*Store)(nil)
	_ notedraft.Repository            = (*Store)(nil)
	_ skill.InvocationRepository      = (*Store)(nil)
	_ workflow.DurableStore           = (*WorkflowStore)(nil)
)
