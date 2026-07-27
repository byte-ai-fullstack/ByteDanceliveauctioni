import { useMemo, useState } from 'react';
import {
  ArrowLeft,
  ArrowRight,
  ClipboardList,
  Coins,
  Eye,
  EyeOff,
  Gavel,
  LockKeyhole,
  LogIn,
  Package,
  Radio,
  ShieldCheck,
  ShoppingBag,
  Sparkles,
  UserPlus,
  UserRound,
} from 'lucide-react';
import { currentAuth, login, logout, registerMerchant, resetPassword } from '../../features/auth/api/authApi';
import { resultMessage } from '../../shared/api/result';
import { canAccessBackoffice } from '../../shared/api/types';
import { clearExpiredMessage, readExpiredMessage } from '../../shared/auth/authStorage';
import { navigateApp } from '../../shared/router/historyStore';

function nextPath(fallback = '/host') {
  const params = new URLSearchParams(location.search);
  const next = params.get('next');
  if (!next || !next.startsWith('/') || next.startsWith('//')) return fallback;
  if (!next.startsWith('/host') && !next.startsWith('/admin')) return fallback;
  return next;
}

const capabilityItems = [
  { icon: <Package size={16} />, label: '拍品准备' },
  { icon: <Gavel size={16} />, label: '竞拍玩法' },
  { icon: <ClipboardList size={16} />, label: '成交处理' },
];

function LoginParticleField() {
  return (
    <div className="authParticleField" aria-hidden="true">
      {Array.from({ length: 64 }).map((_, index) => (
        <i key={index} style={{ '--i': index } as React.CSSProperties} />
      ))}
    </div>
  );
}

function AuctionDecorations() {
  return (
    <div className="authFloatLayer" aria-hidden="true">
      <div className="authFloatCard authLiveCard">
        <small>LIVE AUCTION</small>
        <strong>竞拍中</strong>
        <span>00:20</span>
      </div>
      <div className="authBidStack">
        <span>Bid +¥50</span>
        <span>Bid +¥100</span>
        <span>Bid +¥200</span>
      </div>
      <div className="authProductCard authArtCard">
        <i><Sparkles size={22} /></i>
        <strong>Art Print</strong>
        <span>当前价 ¥199</span>
      </div>
      <div className="authProductCard authBagCard">
        <i><ShoppingBag size={24} /></i>
        <strong>Designer Bag</strong>
        <span>出价 ¥2,880</span>
      </div>
      <div className="authGiftBubble"><ShoppingBag size={34} /></div>
      <div className="authGavelSketch"><Gavel size={112} /></div>
      <div className="authCoin coinA"><Coins size={16} /></div>
      <div className="authCoin coinB"><Coins size={14} /></div>
    </div>
  );
}

export function LoginPage({ embedded = false }: { embedded?: boolean; title?: string }) {
  const redirectTo = useMemo(() => nextPath('/host'), []);
  const [mode, setMode] = useState<'login' | 'register' | 'reset'>('login');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [rememberMe, setRememberMe] = useState(true);
  const [showPassword, setShowPassword] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState(initialLoginError);
  const [message, setMessage] = useState('');

  const currentUser = currentAuth().user;
  const current = canAccessBackoffice(currentUser) ? currentUser : null;
  const needsConfirmPassword = mode === 'register' || mode === 'reset';

  const submit = async () => {
    if (busy) return;
    setBusy(true);
    setError('');
    setMessage('');
    try {
      if (mode === 'register') {
        if (password !== confirmPassword) throw new Error('两次输入的密码不一致');
        await registerMerchant(username.trim(), password);
      } else if (mode === 'reset') {
        if (password !== confirmPassword) throw new Error('两次输入的新密码不一致');
        await resetPassword(username.trim(), password);
        setPassword('');
        setConfirmPassword('');
        setMode('login');
        setMessage('密码已重置，请使用新密码登录');
        return;
      } else {
        await login(username.trim(), password);
      }
      navigateApp(redirectTo, true);
    } catch (e) {
      setError(resultMessage(e));
    } finally {
      setBusy(false);
    }
  };

  const switchMode = (nextMode: 'login' | 'register' | 'reset') => {
    setMode(nextMode);
    setConfirmPassword('');
    setError('');
    setMessage('');
  };

  const switchAccount = async () => {
    setBusy(true);
    setError('');
    try {
      await logout();
      location.reload();
    } catch (e) {
      setError(resultMessage(e));
      setBusy(false);
    }
  };

  return (
    <main className={embedded ? 'loginPage adminOnlyLogin embedded' : 'loginPage adminOnlyLogin'}>
      <LoginParticleField />
      <AuctionDecorations />
      {!embedded && <a className="loginBack" href="/home"><ArrowLeft size={16} /> 返回首页</a>}

      <section className="loginCenter authShell" aria-label="ByteDance LiveAuction 登录入口">
        <aside className="authBrandPanel">
          <div>
            <p className="authKicker"><Radio size={14} /> LIVEAUCTION STUDIO</p>
            <h1>直播间竞拍工作台</h1>
            <p className="authBrandLead">拍品准备、竞拍玩法、直播间中控台、成交处理，从这里开始。</p>
          </div>

          <div className="authBrandScene" aria-hidden="true">
            <div className="authVisualGavel"><Gavel size={88} /></div>
            <div className="authVisualProduct">
              <ShoppingBag size={34} />
              <strong>Premium Lot</strong>
              <span>¥ 1,280</span>
            </div>
            <div className="authVisualBid bidOne">Bid +¥50</div>
            <div className="authVisualBid bidTwo">Bid +¥100</div>
          </div>

          <div className="authCapabilityGrid" aria-label="核心能力">
            {capabilityItems.map((item) => (
              <span key={item.label}>{item.icon}{item.label}</span>
            ))}
          </div>
        </aside>

        <article className={`loginCard adminLoginCard authFormPanel authFormPanel-${current ? 'signed' : mode}`}>
          {current ? (
            <div className="authSignedInState">
              <p className="loginEyebrow"><ShieldCheck size={14} /> ByteDance LiveAuction</p>
              <h2>欢迎回来，{current.nickname}</h2>
              <p className="loginSubcopy">当前账号已登录，系统会根据该账号绑定的团队权限开放拍品准备、直播中控、成交处理、数据复盘等功能。</p>
              {error && <p className="loginError">{error}</p>}
              <div className="authSignedActions">
                <a className="loginSubmit authPrimaryLink" href={redirectTo}>继续进入工作台 <ArrowRight size={17} /></a>
                <button className="loginGhost" type="button" disabled={busy} onClick={switchAccount}>切换账号</button>
              </div>
            </div>
          ) : (
            <>
              <p className="loginEyebrow"><ShieldCheck size={14} /> ByteDance LiveAuction</p>
              <h2>{mode === 'register' ? '注册主账号' : mode === 'reset' ? '重置密码' : '进入工作台'}</h2>
              {mode === 'register' ? <p className="loginSubcopy">每个主播或商家注册一个主账号；主账号登录后再创建自己的团队子账号。</p> : null}
              {mode === 'reset' ? <p className="loginSubcopy">输入账号和新密码，重置后回到登录入口。</p> : null}

              {mode !== 'reset' ? (
                <nav className="loginTabs authModeIndicator" aria-label="当前账号入口">
                  <button type="button" className="active" tabIndex={-1} aria-current="page">{mode === 'register' ? '注册主账号' : '账号登录'}</button>
                </nav>
              ) : null}

              <label className="loginField">
                <span>账号 / 邮箱 / 手机号</span>
                <div><UserRound size={16} /><input value={username} onChange={(e) => setUsername(e.target.value)} placeholder={mode === 'register' ? '请输入主账号' : mode === 'reset' ? '请输入要重置的账号' : '请输入团队账号'} autoComplete="username" /></div>
              </label>
              <label className="loginField">
                <span>{mode === 'reset' ? '新密码' : '密码'}</span>
                <div>
                  <LockKeyhole size={16} />
                  <input value={password} onChange={(e) => setPassword(e.target.value)} placeholder={mode === 'reset' ? '请输入新密码' : '请输入密码'} type={showPassword ? 'text' : 'password'} autoComplete={mode === 'login' ? 'current-password' : 'new-password'} />
                  <button type="button" className="passwordToggle" onClick={() => setShowPassword((x) => !x)} aria-label={showPassword ? '隐藏密码' : '显示密码'}>{showPassword ? <EyeOff size={15} /> : <Eye size={15} />}</button>
                </div>
              </label>
              {needsConfirmPassword ? (
                <label className="loginField">
                  <span>{mode === 'register' ? '确认密码' : '确认新密码'}</span>
                  <div>
                    <LockKeyhole size={16} />
                    <input value={confirmPassword} onChange={(e) => setConfirmPassword(e.target.value)} placeholder={mode === 'register' ? '再次输入密码' : '再次输入新密码'} type={showPassword ? 'text' : 'password'} autoComplete="new-password" />
                  </div>
                </label>
              ) : null}

              <div className="authFormOptions">
                {mode === 'reset' ? (
                  <button type="button" className="authTextButton" onClick={() => switchMode('login')}>返回登录</button>
                ) : (
                  <label><input type="checkbox" checked={rememberMe} onChange={(e) => setRememberMe(e.target.checked)} /> 记住我</label>
                )}
                {mode !== 'reset' ? (
                  <div className="authInlineActions">
                    <button type="button" className="authTextButton" onClick={() => switchMode('reset')}>忘记密码</button>
                    <button type="button" className="authTextButton" onClick={() => switchMode(mode === 'register' ? 'login' : 'register')}>{mode === 'register' ? '账号登录' : '注册主账号'}</button>
                  </div>
                ) : null}
              </div>

              {error && <p className="loginError">{error}</p>}
              {message && <p className="loginSuccess">{message}</p>}

              <button className="loginSubmit" disabled={busy || !username.trim() || !password || (needsConfirmPassword && !confirmPassword)} onClick={submit}>
                {mode === 'register' ? <UserPlus size={17} /> : mode === 'reset' ? <LockKeyhole size={17} /> : <LogIn size={17} />} {busy ? '处理中...' : mode === 'register' ? '创建主账号并进入' : mode === 'reset' ? '重置密码' : '进入 LiveAuction 工作台'}
              </button>

              {mode === 'reset' ? <p className="loginHint">重置成功后，使用新密码重新登录。</p> : null}
            </>
          )}
        </article>
      </section>
    </main>
  );
}

function initialLoginError() {
  const params = new URLSearchParams(location.search);
  if (!params.get('expired')) return '';
  const message = readExpiredMessage('登录已过期，请重新登录');
  clearExpiredMessage();
  return message;
}
