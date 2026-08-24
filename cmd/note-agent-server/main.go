package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/signal"
	"syscall"

	einoagent "github.com/TowDaysZzz/HarnessLoopAgent/internal/agent/eino"
	agentauth "github.com/TowDaysZzz/HarnessLoopAgent/internal/auth"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/chat"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/config"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/contextmanager"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/dailyreview"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/intentexecutor"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/knowledgebase"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/mcpfacade"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/note"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/notedraft"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/httpserver"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/platform/mysqlstore"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/routing"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/runtime"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/tools"
)

func main() {
	if err := run(); err != nil {
		log.Printf("note agent stopped: %v", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agentRunner, err := einoagent.NewConfiguredRunner(ctx, cfg)
	if err != nil {
		if cfg.Memory.Enabled && cfg.Memory.WorkflowPilotEnabled {
			return memoryStartupError("structured model")
		}
		return err
	}

	var serverOptions []httpserver.Option
	var store *mysqlstore.Store
	var memoryRuntime *memoryRuntimeAssembly
	var reminderRuntime *reminderRuntimeAssembly
	if cfg.Database.Enabled {
		store, err = mysqlstore.Open(ctx, mysqlstore.Options{
			DSN: cfg.Database.DSN, MaxOpenConns: cfg.Database.MaxOpenConns,
			MaxIdleConns: cfg.Database.MaxIdleConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
			ProjectionVersion: cfg.Memory.ProjectionVersion,
		})
		if err != nil {
			if cfg.Memory.Enabled {
				return memoryStartupError("database")
			}
			return err
		}
		defer store.Close()
		if cfg.Database.AutoMigrate {
			if err := store.Migrate(ctx); err != nil {
				if cfg.Memory.Enabled {
					return memoryStartupError("database migration")
				}
				return err
			}
		}
		if cfg.Memory.Enabled {
			var memoryRAG ragclient.MemoryClient
			if cfg.Memory.RAGEnabled {
				memoryRAG, err = ragclient.NewClient(ragclient.ClientConfig{
					BaseURL: cfg.Memory.RAGEndpoint, APIKey: cfg.Memory.RAGServiceToken,
					OwnerClaimSecret: cfg.Memory.OwnerClaimSecret, Timeout: cfg.Memory.RAGTimeout,
				})
				if err != nil {
					return memoryStartupError("rag")
				}
			}
			var memoryErr error
			memoryRuntime, memoryErr = assembleMemoryRuntime(cfg.Memory, memoryRuntimeDependencies{
				Repository: store, WorkflowStore: store.WorkflowStore(), EditPayloadStore: store,
				ProjectionBacklog: store, Runner: agentRunner, RAG: memoryRAG,
			})
			if memoryErr != nil {
				return memoryErr
			}
			if memoryRuntime.Capture != nil {
				serverOptions = append(serverOptions,
					httpserver.WithMemoryCaptureService(memoryRuntime.Capture),
					httpserver.WithMemoryChatIntentPilot(cfg.Memory.WorkflowPilotEnabled),
				)
			}
		}
		if cfg.Reminder.Enabled {
			reminderRuntime, err = assembleReminderRuntime(cfg.Reminder, reminderRuntimeDependencies{Repository: store, MemoryRepository: store, WorkflowStore: store.WorkflowStore(), EditPayloadStore: store, Runner: agentRunner})
			if err != nil {
				return err
			}
			reminderRuntime.Start(ctx)
			if reminderRuntime.Command != nil {
				serverOptions = append(serverOptions, httpserver.WithReminderServices(reminderRuntime.Command, store))
			}
		}
		var noteService *note.Service
		if cfg.Auth.Enabled {
			rag, err := ragclient.NewClient(ragclient.ClientConfig{BaseURL: cfg.RAG.BaseURL, APIKey: cfg.RAG.APIKey, Timeout: cfg.RAG.Timeout})
			if err != nil {
				return err
			}
			authService, err := agentauth.NewService(store, rag, cfg.Auth.SessionSecret, cfg.Auth.SessionTTL)
			if err != nil {
				return err
			}
			serverOptions = append(serverOptions, httpserver.WithAuthService(authService, httpserver.AuthCookieConfig{
				Name: cfg.Auth.CookieName, Secure: cfg.Auth.CookieSecure, MaxAge: cfg.Auth.SessionTTL,
			}))
			knowledgeBaseService, err := knowledgebase.NewService(store, rag)
			if err != nil {
				return err
			}
			serverOptions = append(serverOptions, httpserver.WithKnowledgeBaseService(knowledgeBaseService))
			if cfg.Note.Enabled {
				noteService, err = note.NewServiceWithResolver(store, rag, knowledgeBaseService)
				if err != nil {
					return err
				}
				serverOptions = append(serverOptions, httpserver.WithNoteService(noteService))
				registry := tools.NewRegistry()
				_ = registry.Register(tools.Definition{Name: "notes.list", Description: "List the authenticated user's notes", Roles: []string{"*"}, ReadOnly: true, Handler: func(toolCtx context.Context, _ []byte) ([]byte, error) {
					principal, ok := agentauth.PrincipalFromContext(toolCtx)
					if !ok {
						return nil, agentauth.ErrUnauthenticated
					}
					items, err := noteService.List(toolCtx, principal, 20, "")
					if err != nil {
						return nil, err
					}
					return json.Marshal(map[string]any{"items": items})
				}})
				facade, err := mcpfacade.New(registry)
				if err != nil {
					return err
				}
				serverOptions = append(serverOptions, httpserver.WithMCPFacade(facade))
			}
		}

		assembler := contextmanager.NewBoundedAssembler(
			cfg.Context.MaxInputTokens, cfg.Context.MinRecentMessages, contextmanager.ApproxTokenCounter{},
		)
		draftService, err := notedraft.NewService(store, cfg.Agent.NoteDraftTTL)
		if err != nil {
			return err
		}
		complexHandler, err := routing.NewComplexHandler(agentRunner, cfg.Agent.RunTimeout, cfg.Agent.MaxIterations)
		if err != nil {
			return err
		}
		var noteCreateHandler routing.DeterministicHandler = routing.StaticTextHandler{Text: "当前服务未启用笔记写入，请联系管理员检查 NOTE 配置。"}
		var memoryCaptureHandler routing.DeterministicHandler = routing.StaticTextHandler{Text: "当前服务未启用记忆写入。"}
		var memoryRecallHandler routing.DeterministicHandler = routing.StaticTextHandler{Text: "当前服务未启用记忆查询。"}
		var reminderCommandHandler routing.DeterministicHandler = routing.StaticTextHandler{Text: "当前服务未启用提醒写入。"}
		var reminderQueryHandler routing.DeterministicHandler = routing.StaticTextHandler{Text: "当前服务未启用提醒查询。"}
		if noteService != nil {
			noteCreateHandler = intentexecutor.NoteCreateHandler{
				Notes: noteService, Projector: noteService, Drafts: draftService, Summarizer: intentexecutor.RunnerSummarizer{Runner: agentRunner},
			}
		}
		if memoryRuntime != nil {
			if memoryRuntime.Capture != nil {
				memoryCaptureHandler = intentexecutor.MemoryCaptureHandler{Service: memoryRuntime.Capture}
			}
			if memoryRuntime.Adapter != nil && memoryRuntime.Recall != nil {
				memoryRecallHandler = intentexecutor.MemoryRecallHandler{Planner: memoryRuntime.Adapter, Recall: memoryRuntime.Recall}
			}
		}
		if reminderRuntime != nil {
			if reminderRuntime.Command != nil {
				reminderCommandHandler = intentexecutor.ReminderCommandHandler{Service: reminderRuntime.Command}
			}
			if reminderRuntime.Query != nil {
				reminderQueryHandler = intentexecutor.ReminderQueryHandler{Service: reminderRuntime.Query, Limit: 20}
			}
		}
		var skillRegistry *skill.Registry
		var skillExecutor *skill.Executor
		if cfg.Skills.Enabled {
			definitions := []skill.Definition{}
			if cfg.Skills.DailyReviewEnabled {
				review := dailyreview.ReviewWorkflow{
					Reader: dailyreview.ActivityReader{Chat: store, Notes: store, Memory: store}, Cache: store,
					Memory:    dailyreview.RecallAdapter{Service: memoryRuntime.Recall, Candidates: store, Target: cfg.Memory.RecallTarget, MaxContextChars: cfg.Skills.MaxContextChars},
					Generator: dailyreview.StructuredGenerator{Runner: agentRunner, MaxRepairs: cfg.Skills.MaxRepairAttempts, MaxOutputBytes: cfg.Memory.MaxLLMResponseBytes, Timeout: cfg.Agent.RunTimeout},
					Config:    dailyreview.WorkflowConfig{Options: dailyreview.Options{MaxChatMessages: cfg.Skills.MaxChatMessages, PerSession: cfg.Skills.PerSessionMessages, MaxNotes: cfg.Skills.MaxNotes, IncludeMemory: true}, CacheTTL: cfg.Skills.CacheTTL, CacheLease: cfg.Skills.CacheLease, CacheWait: cfg.Skills.CacheWait, MaxSteps: cfg.Skills.MaxSteps, MaxModelCalls: cfg.Skills.MaxModelCalls, MaxToolCalls: cfg.Skills.MaxToolCalls, OutputSchemaVersion: cfg.Skills.SchemaVersion, PromptPolicyVersion: cfg.Skills.PromptPolicyVersion},
					Harness:   &runtime.Metrics{},
				}
				definitions = append(definitions, skill.Definition{ID: "daily_review", Version: skill.Version(cfg.Skills.SkillVersion), Mode: skill.ModeWorkflow, Risk: skill.RiskReadOnly, Dependencies: []skill.Dependency{"chat", "notes", "memory", "model"}, Budget: skill.Budget{Timeout: cfg.Agent.RunTimeout, MaxSteps: cfg.Skills.MaxSteps, MaxModelCalls: cfg.Skills.MaxModelCalls, MaxToolCalls: cfg.Skills.MaxToolCalls, MaxContextBytes: cfg.Skills.MaxContextChars, MaxOutputBytes: cfg.Memory.MaxLLMResponseBytes * 2}, Matcher: dailyreview.Matcher{Timezone: cfg.Skills.Timezone, MaxLookbackDays: cfg.Skills.MaxLookbackDays}, InputCodec: dailyreview.PlanCodec{Timezone: cfg.Skills.Timezone}, OutputCodec: dailyreview.ReportCodec{}, Workflow: review})
			}
			skillRegistry, err = skill.NewRegistry(definitions, map[skill.Dependency]bool{"chat": true, "notes": true, "memory": memoryRuntime != nil && memoryRuntime.Recall != nil, "model": agentRunner != nil})
			if err != nil {
				return err
			}
			skillExecutor, err = skill.NewExecutor(skillRegistry)
			if err != nil {
				return err
			}
		}
		executor, err := routing.NewFacade(routing.HandlerSet{
			NoteCreate: noteCreateHandler, Clarification: routing.ClarificationHandler{}, DeleteRejected: routing.DeleteRejectedHandler{},
			MemoryCapture: memoryCaptureHandler, MemoryRecall: memoryRecallHandler,
			ReminderCreate: reminderCommandHandler, ReminderUpdate: reminderCommandHandler, ReminderCancel: reminderCommandHandler, ReminderQuery: reminderQueryHandler,
			SimpleChat: routing.ConversationHandler{Runner: agentRunner}, SimpleNoteQuery: routing.ConversationHandler{Runner: agentRunner},
			ComplexChat: complexHandler, ComplexNoteQuery: complexHandler,
			Skills: skillExecutor,
		})
		if err != nil {
			return err
		}
		intentRouter := routing.Router{Classifier: routing.Classifier{
			ComplexThreshold: cfg.Agent.IntentComplexThreshold, MinWriteConfidence: cfg.Agent.IntentMinWriteConfidence,
		}, Drafts: draftService, Skills: skillRegistry, MinSkillConfidence: .9}
		chatService, err := chat.NewService(ctx, store, agentRunner, assembler, chat.ServiceOptions{
			MessageHistoryLimit: cfg.Context.MessageHistoryLimit, DefaultModel: cfg.ActiveModel,
			EnableIntentRouting: cfg.Agent.EnableIntentRouting, EnableLegacyRoutingFallback: cfg.Agent.EnableLegacyRoutingFallback,
			Router: intentRouter, Executor: executor,
		})
		if err != nil {
			return err
		}
		serverOptions = append(serverOptions, httpserver.WithChatService(chatService))
	}

	httpServer := httpserver.New(cfg.HTTPAddr, func() bool { return agentRunner != nil && (!cfg.Database.Enabled || store != nil) }, serverOptions...)
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("note agent listening on %s with Hertz", cfg.HTTPAddr)
		serverErr <- httpServer.Run()
	}()

	select {
	case err := <-serverErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return <-serverErr
	}
}
