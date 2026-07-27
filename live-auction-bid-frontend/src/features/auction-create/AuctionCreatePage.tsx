import { useMemo, useState, type ChangeEvent, type ReactNode } from 'react';
import { AlertTriangle, ArrowDown, ArrowUp, CheckCircle2, ChevronLeft, ChevronRight, ImagePlus, Trash2, UploadCloud } from 'lucide-react';
import { AppLink } from '../../shared/router/AppLink';
import { useAppNavigate } from '../../shared/router/historyStore';
import { createDraftLot, deleteUploadedImage, patchDraftLot, queueLot, uploadImage } from '../auction/api/auctionApi';
import { resultMessage } from '../../shared/api/result';
import { formatMoneyText } from '../../shared/lib/format';
import { StudioButton, StudioCard, StudioField, StudioPageHeader, StudioToastViewport } from '../../pages/host-console/components/studio-ui';
import { useStudioToast } from '../../pages/host-console/components/studio-toast';
import { auctionMoney, buildTrustCards, CATEGORY_OPTIONS, CUSTOM_CATEGORY_VALUE, imageFromAsset, initialForm, isBlockingIssue, issueText, parseTags, shortURL, STEP_DEFS, stepIndex, toRequest, TRUST_CARD_DEFS, validate, type FormState, type StepKey, type TrustCardDraft, type TrustCardKey } from './model/auctionCreateForm';

type AuctionCreatePageProps = {
  roomId: string;
  roomName?: string;
};

export function AuctionCreatePage({ roomId, roomName = roomId }: AuctionCreatePageProps) {
  const navigate = useAppNavigate();
  const [form, setForm] = useState<FormState>(initialForm);
  const [activeStep, setActiveStep] = useState<StepKey>('product');
  const [previewImageIndex, setPreviewImageIndex] = useState(0);
  const [submitting, setSubmitting] = useState(false);
  const [uploading, setUploading] = useState<Record<string, boolean>>({});
  const [error, setError] = useState('');
  const { toasts, showToast } = useStudioToast();

  const issues = useMemo(() => validate(form), [form]);
  const blockingIssues = issues.filter(isBlockingIssue);
  const hasError = blockingIssues.length > 0;
  const isUploading = Object.values(uploading).some(Boolean);
  const tagList = useMemo(() => parseTags(form.tags), [form.tags]);
  const trustCards = useMemo(() => buildTrustCards(form), [form]);
  const previewImages = useMemo(() => [
    ...(form.imageUrl ? [{ imageUrl: form.imageUrl, label: '主图' }] : []),
    ...form.gallery.map((image, index) => ({ imageUrl: image.imageUrl, label: `轮播 ${index + 1}` })),
  ], [form.gallery, form.imageUrl]);
  const resolvedPreviewImageIndex = Math.min(previewImageIndex, Math.max(previewImages.length - 1, 0));
  const previewImage = previewImages[resolvedPreviewImageIndex] || previewImages[0];
  const activeStepIndex = STEP_DEFS.findIndex((step) => step.key === activeStep);
  const currentStepBlocking = activeStep === 'review' ? blockingIssues : blockingIssues.filter((issue) => issue.step === activeStep);
  const currentStepHasError = currentStepBlocking.length > 0;
  const canGoBack = activeStepIndex > 0;

  const canEnterStep = (stepKey: StepKey) => {
    const targetIndex = STEP_DEFS.findIndex((step) => step.key === stepKey);
    // Backward: always allowed — return to any previous step freely.
    if (targetIndex <= activeStepIndex) return true;
    // Forward: only allow the immediate next step (no skipping).
    if (targetIndex !== activeStepIndex + 1) return false;
    return !blockingIssues.some((issue) => stepIndex(issue.step) < targetIndex);
  };

  const goToStep = (stepKey: StepKey) => {
    if (canEnterStep(stepKey)) setActiveStep(stepKey);
  };

  const goNext = () => {
    if (currentStepHasError || isUploading) return;
    const nextStep = STEP_DEFS[activeStepIndex + 1];
    if (nextStep) setActiveStep(nextStep.key);
  };

  const goBack = () => {
    const previousStep = STEP_DEFS[activeStepIndex - 1];
    if (previousStep) setActiveStep(previousStep.key);
  };

  const update = (patch: Partial<FormState>) => setForm((current) => ({ ...current, ...patch }));
  const updateCategory = (value: string) => {
    if (value === CUSTOM_CATEGORY_VALUE) {
      update({ categoryMode: 'custom', category: '' });
      return;
    }
    update({ categoryMode: 'preset', category: value });
  };
  const updateTrustCard = (key: TrustCardKey, patch: Partial<TrustCardDraft>) => setForm((current) => ({
    ...current,
    trustCards: { ...current.trustCards, [key]: { ...current.trustCards[key], ...patch } },
  }));

  const uploadFile = async (file: File, target: 'main' | 'gallery' | TrustCardKey) => {
    const uploadKey = target === 'main' || target === 'gallery' ? target : `trust-${target}`;
    if (!file.type.startsWith('image/')) {
      showToast({ tone: 'danger', title: '上传失败', description: '请选择图片文件。' });
      return;
    }
    if (target === 'gallery' && form.gallery.length >= 6) {
      showToast({ tone: 'warning', title: '轮播图已满', description: '最多上传 6 张轮播图。' });
      return;
    }
    setUploading((current) => ({ ...current, [uploadKey]: true }));
    try {
      const asset = await uploadImage(file, { roomId, bizType: target === 'main' ? 'lot_image' : target === 'gallery' ? 'lot_gallery' : 'trust_card' });
      if (target === 'main') {
        if (form.mainImageAssetId) void deleteUploadedImage(form.mainImageAssetId, { silent: true });
        update({ imageUrl: asset.imageUrl, mainImageAssetId: asset.id });
        setPreviewImageIndex(0);
      } else if (target === 'gallery') {
        setForm((current) => ({ ...current, gallery: [...current.gallery, imageFromAsset(asset, file.name)] }));
      } else {
        const previous = form.trustCards[target].assetId;
        if (previous) void deleteUploadedImage(previous, { silent: true });
        updateTrustCard(target, { imageUrl: asset.imageUrl, assetId: asset.id });
      }
      showToast({ tone: 'success', title: '图片已上传', description: file.name });
    } catch (e) {
      showToast({ tone: 'danger', title: '上传失败', description: resultMessage(e) });
    } finally {
      setUploading((current) => ({ ...current, [uploadKey]: false }));
    }
  };

  const handleFile = (event: ChangeEvent<HTMLInputElement>, target: 'main' | 'gallery' | TrustCardKey) => {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = '';
    if (file) void uploadFile(file, target);
  };

  const removeMainImage = () => {
    if (form.mainImageAssetId) void deleteUploadedImage(form.mainImageAssetId, { silent: true });
    update({ imageUrl: '', mainImageAssetId: undefined });
  };

  const removeGalleryImage = (index: number) => {
    const image = form.gallery[index];
    if (image?.assetId) void deleteUploadedImage(image.assetId, { silent: true });
    setForm((current) => ({ ...current, gallery: current.gallery.filter((_, itemIndex) => itemIndex !== index) }));
  };

  const moveGalleryImage = (index: number, direction: -1 | 1) => {
    setForm((current) => {
      const nextIndex = index + direction;
      if (nextIndex < 0 || nextIndex >= current.gallery.length) return current;
      const gallery = [...current.gallery];
      [gallery[index], gallery[nextIndex]] = [gallery[nextIndex], gallery[index]];
      return { ...current, gallery };
    });
  };

  const removeTrustImage = (key: TrustCardKey) => {
    const assetId = form.trustCards[key].assetId;
    if (assetId) void deleteUploadedImage(assetId, { silent: true });
    updateTrustCard(key, { imageUrl: '', assetId: undefined });
  };

  const clearForm = () => {
    const assetIds = [form.mainImageAssetId, ...form.gallery.map((image) => image.assetId), ...TRUST_CARD_DEFS.map((card) => form.trustCards[card.key].assetId)].filter(Boolean) as string[];
    assetIds.forEach((assetId) => void deleteUploadedImage(assetId, { silent: true }));
    setForm(initialForm);
    setActiveStep('product');
    setPreviewImageIndex(0);
    setError('');
  };

  const submit = async () => {
    if (hasError || isUploading) return;
    setSubmitting(true);
    setError('');
    try {
      const draft = await createDraftLot({ roomId });
      const saved = await patchDraftLot(draft.id, toRequest(form, roomId));
      const queued = await queueLot(saved.id);
      showToast({ tone: 'success', title: '拍品已加入本场队列', description: `${queued.lot.title} · #${queued.queuePosition || queued.lot.queuePosition || '-'}` });
      window.setTimeout(() => { void navigate('/admin/auctions?queued=1'); }, 350);
    } catch (e) {
      const message = resultMessage(e);
      setError(message);
      showToast({ tone: 'danger', title: '加入队列失败', description: message });
      setSubmitting(false);
    }
  };

  return <section className="auctionCreatePage">
    <StudioToastViewport toasts={toasts} className="auctionCreateToastViewport" />
    <StudioCard padding="lg" className="auctionCreateTitleBar auctionCreateHeader">
      <StudioPageHeader eyebrow="Create lot" title="添加拍品" actions={<AppLink className="studioButton studioButton-secondary studioButton-md" to="/admin/auctions">返回队列</AppLink>} />
    </StudioCard>
    {error ? <div className="auctionMgmtNotice danger"><AlertTriangle size={16} />{error}</div> : null}
    <nav className="publishStepper auctionCreateStepper" aria-label="添加拍品步骤">
      {STEP_DEFS.map((step, index) => {
        const stepIssues = issues.filter((issue) => issue.step === step.key);
        const hasStepError = stepIssues.some((issue) => issue.level === 'error');
        const hasStepWarning = stepIssues.some((issue) => issue.level === 'warning');
        const isActive = activeStep === step.key;
        const isDone = index < activeStepIndex && !hasStepError;
        const isRisk = hasStepWarning && !hasStepError;
        const isLocked = !canEnterStep(step.key);
        const lockedIssue = blockingIssues.find((issue) => stepIndex(issue.step) < index);
        const stepStatus = isDone ? '已完成' : isRisk ? '需检查' : isActive ? '当前' : '待完成';
        return <button key={step.key} type="button" disabled={isLocked || submitting} title={lockedIssue ? `请先完成：${lockedIssue.text}` : undefined} aria-current={isActive ? 'step' : undefined} className={`${isActive ? 'active' : ''} ${isDone ? 'done' : ''} ${isRisk ? 'risk' : ''} ${isLocked ? 'locked' : ''}`.trim()} onClick={() => goToStep(step.key)}>
          <b>{isDone ? <CheckCircle2 size={16} /> : index + 1}</b>
          <span>{step.label}</span>
          <small>{step.hint}</small>
          <em className="stepStatus">{stepStatus}</em>
        </button>;
      })}
    </nav>
    <div className="auctionCreateLayout">
      <main className="auctionCreateMain">
        {activeStep === 'product' ? <section className="auctionStepCard productInfoWorkbench">
          <header><div><p>Product assets</p><h3>拍品资料与素材</h3></div><div className="auctionStepActions"><StudioButton type="button" variant="secondary" onClick={clearForm} disabled={submitting || isUploading}>清空</StudioButton><StudioButton type="button" variant="primary" icon={<ChevronRight size={15} />} disabled={currentStepHasError || isUploading || submitting} onClick={goNext}>{isUploading ? '等待图片上传完成' : '下一步'}</StudioButton></div></header>
          <div className="productCockpitGrid">
            <section className="productPanel mediaPanel">
              <h4>拍品图片</h4>
              <AuctionField label="拍品主图" error={issueText(issues, '主图')} className="fieldMainImage">
                <div className={`auctionUpload mainImageUpload ${form.imageUrl ? 'hasImage' : ''} ${uploading.main ? 'isUploading' : ''}`}>
                  {form.imageUrl ? <img src={form.imageUrl} alt={form.title || '拍品主图'} /> : <ImagePlus size={34} />}
                  <b>{form.imageUrl ? '主图已上传' : '点击上传主图'}</b>
                  {uploading.main ? <span>上传中</span> : null}
                  <input type="file" accept="image/*" disabled={uploading.main || submitting} onChange={(event) => handleFile(event, 'main')} />
                </div>
                {form.imageUrl ? <div className="mainImageControl"><span>{shortURL(form.imageUrl)}</span><button type="button" disabled={submitting} onClick={removeMainImage}>移除</button></div> : null}
              </AuctionField>
              <AuctionField label="轮播图" help="最多 6 张，按当前顺序展示。" className="fieldCarousel">
                <div className={`auctionUpload carouselUpload ${uploading.gallery ? 'isUploading' : ''}`}>
                  <UploadCloud size={18} /><span>{uploading.gallery ? '上传中' : '上传轮播图'}</span>
                  <input type="file" accept="image/*" disabled={uploading.gallery || submitting || form.gallery.length >= 6} onChange={(event) => handleFile(event, 'gallery')} />
                </div>
                <div className="galleryThumbList">
                  {form.gallery.map((image, index) => <div key={`${image.imageUrl}-${index}`}><img src={image.imageUrl} alt={`轮播图 ${index + 1}`} /><span>#{index + 1}</span><button type="button" disabled={index === 0 || submitting} onClick={() => moveGalleryImage(index, -1)} aria-label="上移轮播图"><ArrowUp size={14} /></button><button type="button" disabled={index === form.gallery.length - 1 || submitting} onClick={() => moveGalleryImage(index, 1)} aria-label="下移轮播图"><ArrowDown size={14} /></button><button type="button" disabled={submitting} onClick={() => removeGalleryImage(index)} aria-label="删除轮播图"><Trash2 size={14} /></button></div>)}
                </div>
              </AuctionField>
            </section>
            <section className="productPanel basePanel">
              <h4>基础资料</h4>
              <div className="baseInfoGrid">
                <AuctionField label="拍品名称" error={issueText(issues, '名称')} className="fieldTitle"><input value={form.title} onChange={(e) => update({ title: e.target.value })} placeholder="请输入竞拍拍品名称" /></AuctionField>
                <AuctionField label="分类" error={issueText(issues, '分类')} className="fieldCategory">
                  <select value={form.categoryMode === 'custom' ? CUSTOM_CATEGORY_VALUE : form.category} onChange={(e) => updateCategory(e.target.value)}>
                    <option value="">请选择拍品分类</option>
                    {CATEGORY_OPTIONS.map((category) => <option key={category} value={category}>{category}</option>)}
                    <option value={CUSTOM_CATEGORY_VALUE}>其他</option>
                  </select>
                  {form.categoryMode === 'custom' ? <input value={form.category} onChange={(e) => update({ category: e.target.value })} placeholder="请输入自定义分类" /> : null}
                </AuctionField>
                <AuctionField label="标签" help="用逗号分隔。" className="fieldTags"><input value={form.tags} onChange={(e) => update({ tags: e.target.value })} placeholder="保真, 稀缺, 福利场" /></AuctionField>
                <AuctionField label="参考估价（元）" className="fieldEstimate"><input type="number" value={form.estimatePrice} min={0} placeholder="可选" onChange={(e) => update({ estimatePrice: e.target.value === '' ? '' : Number(e.target.value) })} /></AuctionField>
                <AuctionField label="库存" error={issueText(issues, '库存')} className="fieldStock"><input type="number" value={form.stock} min={1} onChange={(e) => update({ stock: Number(e.target.value) })} /></AuctionField>
                <AuctionField label="拍品介绍" error={issueText(issues, '介绍')} className="fieldDescription"><textarea value={form.description} onChange={(e) => update({ description: e.target.value })} rows={5} placeholder="描述材质、成色、亮点和竞拍价值" /></AuctionField>
                <AuctionField label="售后说明" className="fieldService"><textarea value={form.afterSaleNotes} onChange={(e) => update({ afterSaleNotes: e.target.value })} rows={3} placeholder="成交后确认、发货、保价、客服承诺等" /></AuctionField>
              </div>
            </section>
          </div>
        </section> : null}
        {activeStep === 'rules' ? <section className="auctionStepCard ruleWorkbench">
          <header><div><p>Auction rules</p><h3>竞拍规则</h3></div><div className="auctionStepActions"><StudioButton type="button" variant="secondary" onClick={clearForm} disabled={submitting || isUploading}>清空</StudioButton><StudioButton type="button" variant="secondary" icon={<ChevronLeft size={15} />} onClick={goBack} disabled={!canGoBack || submitting}>上一步</StudioButton><StudioButton type="button" variant="primary" icon={<ChevronRight size={15} />} disabled={currentStepHasError || isUploading || submitting} onClick={goNext}>{isUploading ? '等待图片上传完成' : '下一步'}</StudioButton></div></header>
          <div className="ruleFieldGrid">
            <StudioField label="起拍价（元）"><input type="number" value={form.startPrice} min={0} onChange={(e) => update({ startPrice: Number(e.target.value) })} /></StudioField>
            <StudioField label="加价幅度（元）" error={issueText(issues, '加价')}><input type="number" value={form.minIncrement} min={1} onChange={(e) => update({ minIncrement: Number(e.target.value) })} /></StudioField>
            <StudioField label="封顶价（元）" error={issueText(issues, '封顶')}><input type="number" value={form.capPrice} placeholder="可选" onChange={(e) => update({ capPrice: e.target.value === '' ? '' : Number(e.target.value) })} /></StudioField>
            <StudioField label="保证金（元）" error={issueText(issues, '保证金')}><input type="number" value={form.depositAmount} min={0} onChange={(e) => update({ depositAmount: Number(e.target.value) })} /></StudioField>
            <StudioField label="竞拍时长（秒）" error={issueText(issues, '时长')}><input type="number" value={form.durationSeconds} min={60} onChange={(e) => update({ durationSeconds: Number(e.target.value) })} /></StudioField>
            <StudioField label="延时窗口（秒）" error={issueText(issues, '延时窗口')}><input type="number" value={form.antiSnipeWindowSeconds} min={1} onChange={(e) => update({ antiSnipeWindowSeconds: Number(e.target.value) })} /></StudioField>
            <StudioField label="每次延长（秒）" error={issueText(issues, '每次延长')}><input type="number" value={form.antiSnipeExtendSeconds} min={10} max={30} onChange={(e) => update({ antiSnipeExtendSeconds: Number(e.target.value) })} /></StudioField>
            <StudioField label="最大延时次数" error={issueText(issues, '最大延时')}><input type="number" value={form.maxExtendCount} min={1} onChange={(e) => update({ maxExtendCount: Number(e.target.value) })} /></StudioField>
          </div>
        </section> : null}
        {activeStep === 'briefing' ? <section className="auctionStepCard liveBriefingWorkbench">
          <header><div><p>Briefing cards</p><h3>主播讲解</h3></div><div className="auctionStepActions"><StudioButton type="button" variant="secondary" onClick={clearForm} disabled={submitting || isUploading}>清空</StudioButton><StudioButton type="button" variant="secondary" icon={<ChevronLeft size={15} />} onClick={goBack} disabled={!canGoBack || submitting}>上一步</StudioButton><StudioButton type="button" variant="primary" icon={<ChevronRight size={15} />} disabled={currentStepHasError || isUploading || submitting} onClick={goNext}>{isUploading ? '等待图片上传完成' : '下一步'}</StudioButton></div></header>
          <section className="productPanel trustPanel">
            <div className="trustCardGrid">
              {TRUST_CARD_DEFS.map((card) => {
                const item = form.trustCards[card.key];
                return <AuctionField key={card.key} label={card.label} className="trustCardField">
                  <textarea value={item.content} onChange={(e) => updateTrustCard(card.key, { content: e.target.value })} rows={3} placeholder={card.placeholder} />
                  <div className={`trustImageSlot ${item.imageUrl ? 'hasImage' : ''} ${uploading[`trust-${card.key}`] ? 'isUploading' : ''}`}>
                    {item.imageUrl ? <img src={item.imageUrl} alt={`${card.label}图片`} /> : <UploadCloud size={16} />}
                    <span>{item.imageUrl ? '图片已上传' : '上传图片'}</span>
                    <input type="file" accept="image/*" disabled={uploading[`trust-${card.key}`] || submitting} onChange={(event) => handleFile(event, card.key)} />
                  </div>
                  {item.imageUrl ? <button className="trustImageRemove" type="button" disabled={submitting} onClick={() => removeTrustImage(card.key)}>移除图片</button> : null}
                </AuctionField>;
              })}
            </div>
          </section>
        </section> : null}
        {activeStep === 'review' ? <section className="auctionStepCard publishReviewWorkbench">
          <header><div><p>Final review</p><h3>确认发布</h3></div><div className="auctionStepActions"><StudioButton type="button" variant="secondary" onClick={clearForm} disabled={submitting || isUploading}>清空</StudioButton><StudioButton type="button" variant="secondary" icon={<ChevronLeft size={15} />} onClick={goBack} disabled={!canGoBack || submitting}>上一步</StudioButton><StudioButton type="button" variant="primary" loading={submitting} disabled={hasError || isUploading} onClick={() => void submit()}>{submitting ? '正在加入本场队列...' : isUploading ? '等待图片上传完成' : '加入本场队列'}</StudioButton></div></header>
          <div className="publishReviewGrid">
            <div className="publishSummaryBlock">
              <h4>入队摘要</h4>
              <div><span>直播间</span><b>{roomName}</b></div>
              <div><span>拍品</span><b>{form.title || '未填写'}</b></div>
              <div><span>分类</span><b>{form.category.trim() || '未选择'}</b></div>
              <div><span>图片素材</span><b>{Number(Boolean(form.imageUrl)) + form.gallery.length} 张</b></div>
              <div><span>讲解卡</span><b>{trustCards.length} 张</b></div>
            </div>
            <div className="publishIssueBox">
              <h4>发布检查</h4>
              {issues.length ? issues.map((issue) => <div key={issue.text} className={issue.level}><AlertTriangle size={15} /><span>{issue.text}</span></div>) : <div className="success"><CheckCircle2 size={15} /><span>核心配置已通过</span></div>}
            </div>
          </div>
        </section> : null}

      </main>
      <aside className="stickyPreviewPanel">
        <div className="mobilePreviewWrap">
          <div className="mobileAuctionPhone h5AuctionPreview">
            <section className="phoneLotCard">
              <div className="phoneImage">
                {previewImage ? <img src={previewImage.imageUrl} alt={`${previewImage.label}预览`} /> : <ImagePlus size={28} />}
                {previewImages.length > 1 ? <>
                  <button className="phoneImageNav prev" type="button" aria-label="上一张预览图" onClick={() => setPreviewImageIndex((resolvedPreviewImageIndex + previewImages.length - 1) % previewImages.length)}><ChevronLeft size={15} /></button>
                  <button className="phoneImageNav next" type="button" aria-label="下一张预览图" onClick={() => setPreviewImageIndex((resolvedPreviewImageIndex + 1) % previewImages.length)}><ChevronRight size={15} /></button>
                </> : null}
              </div>
              {previewImages.length > 1 ? <div className="phoneCarouselStrip">{previewImages.map((image, index) => <button key={`${image.imageUrl}-${index}`} type="button" className={index === resolvedPreviewImageIndex ? 'active' : ''} onClick={() => setPreviewImageIndex(index)}><img src={image.imageUrl} alt={image.label} /><span>{index + 1}</span></button>)}</div> : null}
              <div className="phoneLotInfo">
                <h4>{form.title || '拍品名称待填写'}</h4>
              </div>
              {tagList.length ? <div className="phoneRanking">{tagList.slice(0, 3).map((tag) => <span key={tag}>{tag}</span>)}</div> : null}
              <div className="phonePriceGrid">
                <div><span>当前价</span><b>{activeStep === 'product' ? '待配置' : formatMoneyText(auctionMoney(form.startPrice))}</b></div>
                <div><span>倒计时</span><b>{activeStep === 'product' ? '等待开拍' : `${form.durationSeconds}s`}</b></div>
                <div><span>起拍价</span><b>{activeStep === 'product' ? '待配置' : formatMoneyText(auctionMoney(form.startPrice))}</b></div>
                <div><span>加价幅度</span><b>{activeStep === 'product' ? '待配置' : formatMoneyText(auctionMoney(form.minIncrement))}</b></div>
                <div><span>保证金</span><b>{activeStep === 'product' ? '待配置' : formatMoneyText(auctionMoney(form.depositAmount))}</b></div>
              </div>
              <button type="button">立即出价</button>
            </section>
          </div>
        </div>
        {activeStep === 'rules' || activeStep === 'review' ? <StudioCard title="规则摘要" subtitle="Summary" padding="md"><div className="ruleSnapshotGrid"><div><span>起拍价</span><b>{formatMoneyText(auctionMoney(form.startPrice))}</b></div><div><span>加价幅度</span><b>{formatMoneyText(auctionMoney(form.minIncrement))}</b></div><div><span>封顶价</span><b>{form.capPrice === '' ? '未设置' : formatMoneyText(auctionMoney(form.capPrice))}</b></div><div><span>保证金</span><b>{formatMoneyText(auctionMoney(form.depositAmount))}</b></div><div><span>库存</span><b>{form.stock}</b></div><div><span>最后出价延时</span><b>{form.antiSnipeWindowSeconds}s / +{form.antiSnipeExtendSeconds}s</b></div></div></StudioCard> : null}
      </aside>
    </div>
  </section>;
}

function AuctionField({ label, help, error, children, className = '' }: { label: string; help?: string; error?: string; children: ReactNode; className?: string }) {
  return <div className={`auctionField ${className}`.trim()}><span>{label}</span>{children}{help ? <small>{help}</small> : null}{error ? <em>{error}</em> : null}</div>;
}
