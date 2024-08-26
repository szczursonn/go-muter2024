package muterbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

type muteExecutor struct {
	client *discordgo.Session
	userId string
}

type muterBot struct {
	client    *discordgo.Session
	executors []*muteExecutor
	prefix    string

	ctx       context.Context
	cancelCtx context.CancelFunc
	wg        sync.WaitGroup

	muteMu sync.Mutex
}

const cleanupDelay = 15 * time.Second

func RunMuterBot(ctx context.Context, doneChan chan error, tokens []string, prefix string) {
	bot := &muterBot{
		prefix: prefix,
	}

	if len(tokens) == 0 {
		doneChan <- fmt.Errorf("no tokens provided")
		return
	}

	bot.executors = []*muteExecutor{}
	for i, token := range tokens[1:] {
		client, err := discordgo.New(fmt.Sprintf("Bot %s", token))
		if err != nil {
			doneChan <- fmt.Errorf("auxiliary client %d failed to initialize: %w", i+1, err)
			return
		}

		clientUser, err := client.User("@me")
		if err != nil {
			doneChan <- fmt.Errorf("failed to get auxiliary user %d info: %w", i+1, err)
			return
		}

		zap.S().Infof("Auxiliary client initialized: %s", clientUser)

		bot.executors = append(bot.executors, &muteExecutor{
			client: client,
			userId: clientUser.ID,
		})
	}

	var err error
	bot.client, err = discordgo.New(fmt.Sprintf("Bot %s", tokens[0]))
	if err != nil {
		doneChan <- fmt.Errorf("primary client failed to initialize: %w", err)
		return
	}

	bot.client.Identify.Intents = discordgo.IntentGuilds | discordgo.IntentGuildMembers | discordgo.IntentGuildVoiceStates | discordgo.IntentGuildPresences | discordgo.IntentGuildMessages
	bot.client.AddHandler(bot.handleMessageCreate)
	if err := bot.client.Open(); err != nil {
		doneChan <- fmt.Errorf("primary client failed to connect to discord: %w", err)
		return
	}

	zap.S().Infof("Primary client logged in as %s", bot.client.State.User)
	bot.executors = append(bot.executors, &muteExecutor{
		client: bot.client,
		userId: bot.client.State.User.ID,
	})

	bot.ctx, bot.cancelCtx = context.WithCancel(ctx)
	bot.client.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		bot.cancelCtx()
	})
	go func() {
		<-bot.ctx.Done()
		bot.cancelCtx()
		bot.wg.Wait()
		doneChan <- bot.client.Close()
	}()
}

var commandToNewMuteState = map[string]bool{
	"m":      true,
	"mu":     true,
	"mute":   true,
	"u":      false,
	"um":     false,
	"unmute": false,
}

func (bot *muterBot) handleMessageCreate(_ *discordgo.Session, msg *discordgo.MessageCreate) {
	if msg.Author.Bot || msg.GuildID == "" || msg.ChannelID == "" || !strings.HasPrefix(msg.Content, bot.prefix) {
		return
	}

	bot.wg.Add(1)
	defer bot.wg.Done()
	bot.muteMu.Lock()
	defer bot.muteMu.Unlock()

	command, _ := strings.CutPrefix(msg.Content, bot.prefix)
	newMuteState, ok := commandToNewMuteState[command]
	if !ok {
		return
	}

	senderVoiceState, err := bot.client.State.VoiceState(msg.GuildID, msg.Author.ID)
	if err != nil {
		if !errors.Is(err, discordgo.ErrStateNotFound) {
			zap.S().Errorf("Failed to get sender voice state: %s", err)
		}
		bot.sendReplyAndCleanup(msg.Message, ":x: **You must be in a voice channel**")
		return
	}

	guild, err := bot.client.State.Guild(msg.GuildID)
	if err != nil {
		zap.S().Errorf("Failed to get guild: %s", err)
		return
	}

	targetUserIds := []string{}
	for _, voiceState := range guild.VoiceStates {
		member, _ := bot.client.State.Member(guild.ID, voiceState.UserID)
		if voiceState.ChannelID == senderVoiceState.ChannelID && member != nil && !member.User.Bot && voiceState.Mute != newMuteState {
			targetUserIds = append(targetUserIds, member.User.ID)
		}
	}

	if len(targetUserIds) == 0 {
		bot.sendReplyAndCleanup(msg.Message, ":x: **No one to mute/unmute**")
		return
	}

	availableExecutors := []*muteExecutor{}
	for _, executor := range bot.executors {
		permissions, err := bot.client.UserChannelPermissions(executor.userId, senderVoiceState.ChannelID)
		if err != nil {
			zap.S().Errorf("Failed to get permissions for user %s, channel %s: %s", executor.userId, senderVoiceState.ChannelID, err)
			continue
		}

		if permissions&discordgo.PermissionVoiceMuteMembers != discordgo.PermissionVoiceMuteMembers {
			zap.S().Debugf("Bot %s has no mute permission for channel %s", executor.userId, senderVoiceState.ChannelID)
			continue
		}

		availableExecutors = append(availableExecutors, executor)
	}

	if len(availableExecutors) == 0 {
		bot.sendReplyAndCleanup(msg.Message, ":x: **No clients available**")
		return
	}

	muteWg := sync.WaitGroup{}
	muteWg.Add(len(targetUserIds))
	statsMu := sync.Mutex{}
	successCount := 0

	startTime := time.Now()

	for i := range targetUserIds {
		go func(i int) {
			defer muteWg.Done()
			userId := targetUserIds[i]
			executor := availableExecutors[i%len(availableExecutors)]

			err := executor.client.GuildMemberMute(guild.ID, userId, newMuteState)
			if err != nil {
				zap.S().Errorf("[%s->%s] Failed to set mute to %t: %s", executor.userId, userId, newMuteState, err)
			} else {
				statsMu.Lock()
				successCount++
				statsMu.Unlock()
				zap.S().Infof("[%s->%s] Set mute to %t", executor.userId, userId, newMuteState)
			}
		}(i)
	}
	muteWg.Wait()

	bot.sendReplyAndCleanup(msg.Message, fmt.Sprintf(":salad: **Set mute to %t for %d users, took %s with %d clients**", newMuteState, successCount, time.Since(startTime), len(availableExecutors)))
	zap.S().Infof("Mute done at %s", msg.ChannelID)
}

func (bot *muterBot) scheduleMessageCleanup(msg *discordgo.Message) {
	bot.wg.Add(1)
	go func() {
		defer bot.wg.Done()
		select {
		case <-time.After(cleanupDelay):
			break
		case <-bot.ctx.Done():
			break
		}

		err := bot.client.ChannelMessageDelete(msg.ChannelID, msg.ID)
		if err != nil {
			zap.S().Errorf("Failed to cleanup message %s: %s", msg.ID, err)
		} else {
			zap.S().Debugf("Cleaned up %s", msg.ID)
		}
	}()
}

func (bot *muterBot) sendReplyAndCleanup(msg *discordgo.Message, content string) {
	replyMsg, err := bot.client.ChannelMessageSendReply(msg.ChannelID, content, msg.Reference())
	if err != nil {
		zap.S().Errorf("Failed to send reply: %s", err)
	} else {
		zap.S().Debugf("Reply sent: %s", replyMsg.ID)
		bot.scheduleMessageCleanup(replyMsg)
	}
	bot.scheduleMessageCleanup(msg)
}
