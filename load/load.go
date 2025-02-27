// Copyright (c) 2024 The Flokicoin developers
// Distributed under the MIT software license, see the accompanying
// file COPYING or http://www.opensource.org/licenses/mit-license.php.

package load

import (
	"fmt"
	"sync"
	"time"

	"github.com/flokiorg/walletd/chain/electrum"
	wlt "github.com/flokiorg/walletd/wallet"
	"github.com/flokiorg/walletd/walletmgr"
	"github.com/gdamore/tcell/v2"

	"github.com/rivo/tview"

	. "github.com/flokiorg/twallet/shared"
)

type AppConfig struct {
	WalletDir      string        `short:"d" long:"walletdir"  description:"Directory for the wallet.db"`
	RegressionTest bool          `long:"regtest" description:"Use the regression test network"`
	Testnet        bool          `long:"testnet" description:"Use the test network"`
	DBTimeout      time.Duration `short:"t" long:"timeout" description:"Timeout duration (in seconds) for database connections."`
	ElectrumServer string        `short:"e" long:"electserver" description:"Electrum server host:port"`
	ConfigFile     string        `short:"c" long:"config" description:"Path to configuration file"`
	AccountID      uint32        `short:"a" description:"Account ID"`
}

type AppInfo struct {
	Config *AppConfig
	Params *walletmgr.WalletParams
}

func NewAppInfo(cfg *AppConfig, params *walletmgr.WalletParams) *AppInfo {
	return &AppInfo{
		Config: cfg,
		Params: params,
	}
}

type Load struct {
	*AppInfo
	*tview.Application
	Nav    *Navigator
	Wallet Wallet
	Notif  *notification
	tm     *tryManager
}

func NewLoad(appInfo *AppInfo, wallet Wallet, tapp *tview.Application, pages *tview.Pages) *Load {
	l := &Load{
		AppInfo:     appInfo,
		Application: tapp,
		Nav:         NewNavigator(tapp, pages),
		Wallet:      wallet,
		Notif:       newNotification(wallet),
		tm:          newTryManager(),
	}

	l.Application.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() != tcell.KeyESC {
			return event
		}
		l.Notif.CancelToast()
		l.Nav.CloseModal()
		l.Application.SetFocus(l.Nav.pages)
		return event
	})

	return l
}

func (l *Load) StartSync() {
	if err := l.Wallet.Synchronize(); err != nil {
		l.Restart()
	}
}

func (l *Load) Restart() {
	l.tm.try(func() error {
		l.Notif.healthToast <- "Restarting..."
		l.Notif.healthNotif <- electrum.NerrHealthRestarting
		time.Sleep(time.Second * 2)
		return l.Wallet.Synchronize()
	}, l.Notif)
}

type notification struct {
	accountNotif   <-chan *wlt.AccountNotification
	txNotif        <-chan *wlt.TransactionNotifications
	spentNessNotif <-chan *wlt.SpentnessNotifications
	healthNotif    chan error

	healthToast chan string
	toast       chan string

	mu   sync.Mutex
	subs []chan struct{}
	stop chan struct{}
	wg   sync.WaitGroup
}

func (n *notification) Subscribe() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()

	ch := make(chan struct{}, 1)
	n.subs = append(n.subs, ch)
	return ch
}

func newNotification(wallet Wallet) *notification {
	n := &notification{
		healthToast: make(chan string, 5),
		toast:       make(chan string, 5),
		subs:        make([]chan struct{}, 0),
		stop:        make(chan struct{}),
	}

	n.accountNotif, n.txNotif, n.spentNessNotif, n.healthNotif = wallet.Watch()

	go n.listen()

	return n
}

func (n *notification) BroadcastWalletUpdate() {
	n.mu.Lock()
	defer n.mu.Unlock()

	for _, ch := range n.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (n *notification) listen() {

	for {
		select {
		case <-n.accountNotif:
		case <-n.txNotif:
		case <-n.spentNessNotif:
		case <-n.stop:
			return
		}

		n.BroadcastWalletUpdate()
	}
}

func (n *notification) Shutdown() {
	n.mu.Lock()
	defer n.mu.Unlock()

	close(n.stop)
	for _, ch := range n.subs {
		close(ch)
	}
}

func (n *notification) ElectrumHealth() <-chan error {
	return n.healthNotif
}

func (n *notification) Toast() <-chan string {
	return n.toast
}

func (n *notification) ElectrumToast() <-chan string {
	return n.healthToast
}

func (n *notification) ShowToast(text string) {
	n.toast <- text
}

func (n *notification) CancelToast() {
	n.toast <- ""
}

func (n *notification) ShowToastWithTimeout(text string, d time.Duration) {
	n.toast <- text
	go func() {
		time.Sleep(d)
		n.toast <- ""
	}()
}

type tryManager struct {
	mu        sync.Mutex
	isRunning bool
}

func newTryManager() *tryManager {
	return &tryManager{}
}

func (tm *tryManager) try(operation func() error, notif *notification) error {
	tm.mu.Lock()
	if tm.isRunning {
		tm.mu.Unlock()
		return nil // call ignored: operation already in progress
	}
	tm.isRunning = true
	tm.mu.Unlock()

	defer func() {
		tm.mu.Lock()
		tm.isRunning = false
		tm.mu.Unlock()
	}()

	var a, b int = 0, 1

	for {
		err := operation()
		if err == nil {
			notif.healthToast <- ""
			break
		}

		// Operation failed, log the error
		// fmt.Printf("Operation failed: %v\n", err)
		notif.healthNotif <- err

		// Calculate the next Fibonacci delay
		sleepDuration := time.Duration(b) * time.Second
		if sleepDuration > 30*time.Second {
			sleepDuration = 30 * time.Second
		}

		// Sleep before retrying
		notif.healthToast <- fmt.Sprintf("Retrying in %v...\n", sleepDuration)
		time.Sleep(sleepDuration)

		// Move to the next Fibonacci number
		next := a + b
		a = b
		b = next
	}

	return nil
}
