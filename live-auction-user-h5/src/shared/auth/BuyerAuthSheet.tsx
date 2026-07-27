import { useState } from 'react';
import type { FormEvent, MouseEvent } from 'react';
import { normalizeBuyerUsername, validateBuyerCredentials } from './credentialRules';
import { useAuthSession } from './useAuthSession';

type AuthMode = 'login' | 'register' | 'reset';

type BuyerAuthSheetProps = {
  title: string;
  description: string;
  actionLabel: string;
  onAuthenticated?: () => void;
  onClose?: () => void;
};

export function BuyerAuthSheet({ title, description, actionLabel, onAuthenticated, onClose }: BuyerAuthSheetProps) {
  const { loginBuyer, registerBuyer, resetBuyerPassword, reason } = useAuthSession();
  const [authMode, setAuthMode] = useState<AuthMode>('login');
  const [authUsername, setAuthUsername] = useState('');
  const [authPassword, setAuthPassword] = useState('');
  const [authNickname, setAuthNickname] = useState('');
  const [authBusy, setAuthBusy] = useState(false);
  const [authError, setAuthError] = useState('');

  const submitAuth = async (event: FormEvent) => {
    event.preventDefault();
    if (authBusy) return;
    const validationError = validateBuyerCredentials({
      username: authUsername,
      password: authPassword,
      nickname: authNickname,
      requireNickname: authMode === 'register',
    });
    if (validationError) {
      setAuthError(validationError);
      return;
    }

    setAuthBusy(true);
    setAuthError('');
    try {
      const username = normalizeBuyerUsername(authUsername);
      if (authMode === 'reset') {
        await resetBuyerPassword(username, authPassword);
        setAuthMode('login');
        setAuthPassword('');
        setAuthError('密码已重置，请用新密码登录');
        return;
      }
      if (authMode === 'login') await loginBuyer(username, authPassword);
      else await registerBuyer(username, authPassword, authNickname.trim());
      onAuthenticated?.();
    } catch (err) {
      setAuthError(err instanceof Error ? err.message : '登录失败，请重试');
    } finally {
      setAuthBusy(false);
    }
  };

  const closeFromMask = (event: MouseEvent<HTMLDivElement>) => {
    if (event.currentTarget === event.target) onClose?.();
  };

  return (
    <div className="dyMallOrderAuthMask" role="presentation" onClick={closeFromMask}>
      <section className="dyMallOrderAuthSheet" aria-modal="true" role="dialog" aria-label={title}>
        <h2>{title}</h2>
        <p>{description}</p>
        <nav aria-label="登录方式">
          <button type="button" className={authMode === 'login' ? 'isActive' : ''} onClick={() => setAuthMode('login')}>登录</button>
          <button type="button" className={authMode === 'register' ? 'isActive' : ''} onClick={() => setAuthMode('register')}>注册</button>
          <button type="button" className={authMode === 'reset' ? 'isActive' : ''} onClick={() => setAuthMode('reset')}>重置</button>
        </nav>
        <form onSubmit={submitAuth}>
          <input value={authUsername} onChange={(event) => setAuthUsername(event.target.value)} placeholder="请输入账号" autoComplete="username" />
          <input value={authPassword} onChange={(event) => setAuthPassword(event.target.value)} placeholder="请输入密码" type="password" autoComplete={authMode === 'login' ? 'current-password' : 'new-password'} />
          {authMode === 'register' ? (
            <input value={authNickname} onChange={(event) => setAuthNickname(event.target.value)} placeholder="请输入昵称" autoComplete="nickname" />
          ) : null}
          {(authError || reason) ? <p role="alert">{authError || reason}</p> : null}
          <button type="submit" disabled={authBusy}>
            {authBusy ? '处理中...' : authMode === 'login' ? `登录并${actionLabel}` : authMode === 'reset' ? '重置密码' : `注册并${actionLabel}`}
          </button>
        </form>
      </section>
    </div>
  );
}
