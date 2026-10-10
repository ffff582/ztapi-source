import { CheckCircle2, CircleAlert, CircleDollarSign, Copy, Download, FilePlus2, Image as ImageIcon, Images, MessageCircle, Paperclip, Play, Plus, RefreshCw, RotateCcw, Send, Settings2, Sparkles, Video as VideoIcon } from 'lucide-react';
import { ChangeEvent, DragEvent as ReactDragEvent, FormEvent, PointerEvent as ReactPointerEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { apiClient, getAuthSession } from '../../api/client';
import { parseUserLogPage, parseUserModelCatalog, type UserModelCatalogItem, type UserModelSupportedOptions } from '../../api/contracts';
import {
  PlaygroundClientError,
  playgroundClient,
  type PlaygroundChatResult,
  type PlaygroundImageResult,
  type PlaygroundVideoTask,
} from '../../api/playground';
import { useLocale } from '../../i18n/locale';
import { markModelSelected } from '../onboarding/onboarding';

type PlaygroundMode = 'text' | 'image' | 'video';

const defaultPrompt = '';
const imageDefaultPrompt = '';
const videoDefaultPrompt = '';
const VIDEO_POLL_INTERVAL_MS = 1200;
type ReferenceImage = {
  file: File;
  previewUrl: string;
  objectUrl?: string;
};

type ImageEditInput = NonNullable<UserModelSupportedOptions['edit_input']>;

function defaultPromptForMode(mode: PlaygroundMode) {
  if (mode === 'image') return imageDefaultPrompt;
  if (mode === 'video') return videoDefaultPrompt;
  return defaultPrompt;
}

function isWorkbenchModel(item: UserModelCatalogItem) {
  if (item.modality === 'text') return item.supported_endpoint_types.includes('openai');
  if (item.modality === 'image') return item.supported_endpoint_types.includes('images');
  if (item.modality === 'video') return item.supported_endpoint_types.includes('video-tasks');
  return false;
}

function modeForModel(item: UserModelCatalogItem): PlaygroundMode | null {
  return item.modality === 'text' || item.modality === 'image' || item.modality === 'video' ? item.modality : null;
}

function testErrorMessage(error: unknown) {
  if (error instanceof PlaygroundClientError) {
    switch (error.kind) {
      case 'insufficient_balance':
        return '余额不足，请先充值后再测试。';
      case 'model_unavailable':
        return '当前模型暂不可用，请更换模型或稍后再试。';
      case 'rate_limited':
        return '请求较多，请稍后再试。';
      case 'unauthorized':
        return '登录状态已失效，请重新登录。';
      case 'invalid_response':
      case 'unknown':
        break;
    }
  }
  return '测试请求失败，请稍后重试。';
}

function codeExample(mode: PlaygroundMode, model: string) {
  const selectedModel = model || 'YOUR_MODEL_ID';
  if (mode === 'image') {
    return `export ZTAPI_API_KEY="YOUR_ZTAPI_API_KEY"

curl https://ztapi.vip/v1/images/generations \\
  -H "Authorization: Bearer $ZTAPI_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${selectedModel}",
    "prompt": "一只在雨中的橘猫",
    "size": "1024x1024"
  }'`;
  }
  if (mode === 'video') {
    return `export ZTAPI_API_KEY="YOUR_ZTAPI_API_KEY"

curl https://ztapi.vip/v1/video/generations \\
  -H "Authorization: Bearer $ZTAPI_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${selectedModel}",
    "prompt": "镜头缓慢推进一片森林",
    "size": "1280x720",
    "duration": 5
  }'`;
  }
  return `export ZTAPI_API_KEY="YOUR_ZTAPI_API_KEY"

curl https://ztapi.vip/v1/chat/completions \\
  -H "Authorization: Bearer $ZTAPI_API_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{
    "model": "${selectedModel}",
    "messages": [{"role": "user", "content": "你好"}]
  }'`;
}

function firstOption(options: UserModelSupportedOptions | undefined, key: 'sizes' | 'qualities' | 'response_formats' | 'resolutions') {
  return options?.[key]?.[0] ?? '';
}

type ImageRenderOption = NonNullable<UserModelSupportedOptions['render_options']>[number];

function imagePresentationFromSize(size: string, quality = '') {
  if (size === 'auto') return { aspect_ratio: 'auto', resolution: 'auto' };
  const [rawWidth, rawHeight] = size.split('x');
  const width = Number(rawWidth);
  const height = Number(rawHeight);
  if (!Number.isInteger(width) || !Number.isInteger(height) || width <= 0 || height <= 0) {
    return { aspect_ratio: size, resolution: 'custom' };
  }
  let left = width;
  let right = height;
  while (right !== 0) {
    [left, right] = [right, left % right];
  }
  const resolutionFromQuality = ['1k', '2k', '4k'].includes(quality.toLowerCase()) ? quality.toUpperCase() : '';
  return {
    aspect_ratio: `${width / left}:${height / left}`,
    resolution: resolutionFromQuality || `${Math.ceil(Math.max(width, height) / 1024)}K`,
  };
}

function getImageRenderOptions(options: UserModelSupportedOptions | undefined): ImageRenderOption[] {
  if (options?.render_options !== undefined && options.render_options.length > 0) {
    return options.render_options;
  }
  const seenPresentation = new Set<string>();
  return (options?.sizes ?? []).flatMap((size) => (options?.qualities ?? []).map((quality) => ({
      ...imagePresentationFromSize(size, quality),
      size,
      quality,
    })).filter((option) => {
      const key = `${option.aspect_ratio}\u0000${option.resolution}`;
      if (seenPresentation.has(key)) return false;
      seenPresentation.add(key);
      return true;
    }));
}

function videoStatusLabel(status: PlaygroundVideoTask['status']) {
  switch (status) {
    case 'queued':
      return '视频任务已提交，正在等待上游处理。';
    case 'processing':
      return '视频正在生成，请保持页面打开。';
    case 'succeeded':
      return '视频已生成';
    case 'failed':
      return '视频生成失败';
    default:
      return '正在确认视频任务状态。';
  }
}

function imageSource(image: PlaygroundImageResult['images'][number]) {
  if (image.url) return image.url;
  return image.b64_json ? `data:image/png;base64,${image.b64_json}` : '';
}

function formatBytes(value: number) {
  if (value >= 1024 * 1024) return `${(value / (1024 * 1024)).toFixed(1)} MB`;
  if (value >= 1024) return `${Math.max(1, Math.round(value / 1024))} KB`;
  return `${value} B`;
}

function revokeReferencePreview(reference: ReferenceImage) {
  if (reference.objectUrl !== undefined && typeof URL.revokeObjectURL === 'function') {
    URL.revokeObjectURL(reference.objectUrl);
  }
}

function createReferencePreview(file: File): ReferenceImage {
  if (typeof URL.createObjectURL === 'function') {
    const objectUrl = URL.createObjectURL(file);
    return { file, previewUrl: objectUrl, objectUrl };
  }
  return { file, previewUrl: `data:${file.type || 'application/octet-stream'};base64,` };
}

function imageEditCapability(item: UserModelCatalogItem | null): ImageEditInput | null {
  if (item?.modality !== 'image' || item.supported_options?.supports_edits !== true) return null;
  return item.supported_options.edit_input ?? null;
}

function referenceValidationError(
  existing: ReferenceImage[],
  files: File[],
  limits: ImageEditInput,
) {
  if (existing.length + files.length > limits.max_files) {
    return `最多上传 ${limits.max_files} 张参考图。`;
  }
  for (const file of files) {
    if (!limits.mime_types.includes(file.type)) {
      return `不支持 ${file.type || '未知'} 格式，请上传 JPG、PNG 或 WebP。`;
    }
    if (file.size > limits.max_bytes) {
      return `${file.name} 超过单张 ${formatBytes(limits.max_bytes)} 的大小限制。`;
    }
  }
  const totalBytes = existing.reduce((total, item) => total + item.file.size, 0) + files.reduce((total, file) => total + file.size, 0);
  if (totalBytes > limits.max_total_bytes) {
    return `参考图总大小不能超过 ${formatBytes(limits.max_total_bytes)}。`;
  }
  return '';
}

type PlaygroundPageProps = {
  initialMode?: PlaygroundMode;
  workbench?: boolean;
};

type TextConversationMessage = {
  role: 'user' | 'assistant';
  content: string;
};

type TextConversation = {
  id: string;
  title: string;
  model: string;
  updatedAt: number;
  messages: TextConversationMessage[];
};

const TEXT_CONVERSATIONS_STORAGE_PREFIX = 'ztapi:workbench:text-conversations:v1:';
const MAX_TEXT_CONVERSATIONS = 20;

function textConversationsStorageKey(userID: number | undefined) {
  return userID === undefined ? '' : `${TEXT_CONVERSATIONS_STORAGE_PREFIX}${userID}`;
}

function isTextConversationMessage(value: unknown): value is TextConversationMessage {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = value as Partial<TextConversationMessage>;
  return (candidate.role === 'user' || candidate.role === 'assistant') && typeof candidate.content === 'string';
}

function readTextConversations(userID: number | undefined): TextConversation[] {
  const key = textConversationsStorageKey(userID);
  if (key === '' || typeof window === 'undefined') return [];
  try {
    const stored = JSON.parse(window.localStorage.getItem(key) ?? 'null') as unknown;
    if (!Array.isArray(stored)) return [];
    return stored
      .filter((value): value is TextConversation => {
        if (typeof value !== 'object' || value === null) return false;
        const candidate = value as Partial<TextConversation>;
        return typeof candidate.id === 'string' && typeof candidate.title === 'string' &&
          typeof candidate.model === 'string' && typeof candidate.updatedAt === 'number' &&
          Array.isArray(candidate.messages) && candidate.messages.some(isTextConversationMessage);
      })
      .map((conversation) => ({
        ...conversation,
        messages: conversation.messages.filter(isTextConversationMessage),
      }))
      .filter((conversation) => conversation.messages.length > 0)
      .slice(0, MAX_TEXT_CONVERSATIONS);
  } catch {
    return [];
  }
}

function writeTextConversations(userID: number | undefined, conversations: TextConversation[]) {
  const key = textConversationsStorageKey(userID);
  if (key === '' || typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(key, JSON.stringify(conversations.slice(0, MAX_TEXT_CONVERSATIONS)));
  } catch {
    // An unavailable localStorage must not block a paid request.
  }
}

function createTextConversationID() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  return `text-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

function textConversationTitle(prompt: string) {
  return prompt.trim().replace(/\s+/g, ' ').slice(0, 48) || '新的文本对话';
}

export function PlaygroundPage({ initialMode = 'text', workbench = false }: PlaygroundPageProps) {
  const { t } = useLocale();
  const [searchParams] = useSearchParams();
  const [models, setModels] = useState<UserModelCatalogItem[]>([]);
  const [catalogStatus, setCatalogStatus] = useState<'loading' | 'ready' | 'error'>('loading');
  const [mode, setMode] = useState<PlaygroundMode>(initialMode);
  const [modelByMode, setModelByMode] = useState<Record<PlaygroundMode, string>>({ text: '', image: '', video: '' });
  const [promptByMode, setPromptByMode] = useState<Record<PlaygroundMode, string>>({
    text: defaultPromptForMode('text'),
    image: defaultPromptForMode('image'),
    video: defaultPromptForMode('video'),
  });
  const [requestStatus, setRequestStatus] = useState<'idle' | 'sending' | 'success' | 'error'>('idle');
  const [chatResult, setChatResult] = useState<PlaygroundChatResult | null>(null);
  const [imageResult, setImageResult] = useState<PlaygroundImageResult | null>(null);
  const [videoTask, setVideoTask] = useState<PlaygroundVideoTask | null>(null);
  const [billedAmount, setBilledAmount] = useState<number | null>(null);
  const [elapsedMs, setElapsedMs] = useState<number | null>(null);
  const [errorMessage, setErrorMessage] = useState('');
  const [copyState, setCopyState] = useState<'idle' | 'success' | 'error'>('idle');
  const [imageSize, setImageSize] = useState('');
  const [imageQuality, setImageQuality] = useState('');
  const [imageCount, setImageCount] = useState(1);
  const [imageResponseFormat, setImageResponseFormat] = useState('url');
  const [imageHistory, setImageHistory] = useState<PlaygroundImageResult[]>([]);
  const [videoSize, setVideoSize] = useState('');
  const [videoDuration, setVideoDuration] = useState(5);
  const [videoSequence, setVideoSequence] = useState(0);
  const [videoStartedAt, setVideoStartedAt] = useState<number | null>(null);
  const [textAttachmentName, setTextAttachmentName] = useState('');
  const [textConversations, setTextConversations] = useState<TextConversation[]>([]);
  const [activeTextConversationID, setActiveTextConversationID] = useState<string | null>(null);
  const [textHistoryReady, setTextHistoryReady] = useState(!workbench || initialMode !== 'text');
  const [referenceImages, setReferenceImages] = useState<ReferenceImage[]>([]);
  const [referenceError, setReferenceError] = useState('');
  const textAttachmentInput = useRef<HTMLInputElement>(null);
  const referenceInput = useRef<HTMLInputElement>(null);
  const referenceImagesRef = useRef<ReferenceImage[]>([]);
  const requestSequence = useRef(0);

  const model = modelByMode[mode];
  const modeModels = useMemo(
    () => models.filter((item) => modeForModel(item) === mode),
    [mode, models],
  );
  const selectedCatalog = useMemo(
    () => modeModels.find((item) => item.model_name === model) ?? null,
    [model, modeModels],
  );
  const selectedOptions = selectedCatalog?.supported_options;
  const imageRenderOptions = useMemo(() => getImageRenderOptions(selectedOptions), [selectedOptions]);
  const selectedImageRenderOption = useMemo(
    () => imageRenderOptions.find((option) => option.size === imageSize && option.quality === imageQuality) ?? imageRenderOptions[0],
    [imageQuality, imageRenderOptions, imageSize],
  );
  const imageAspectRatios = useMemo(
    () => [...new Set(imageRenderOptions.map((option) => option.aspect_ratio))],
    [imageRenderOptions],
  );
  const imageResolutions = useMemo(
    () => [...new Set(imageRenderOptions.map((option) => option.resolution))],
    [imageRenderOptions],
  );
  const editInput = imageEditCapability(selectedCatalog);
  const example = useMemo(() => codeExample(mode, model), [mode, model]);
  const prompt = promptByMode[mode];
  const userID = getAuthSession()?.user.id;
  const activeTextConversation = textConversations.find((conversation) => conversation.id === activeTextConversationID) ?? null;

  function setPrompt(nextPrompt: string | ((current: string) => string)) {
    setPromptByMode((current) => ({
      ...current,
      [mode]: typeof nextPrompt === 'function' ? nextPrompt(current[mode]) : nextPrompt,
    }));
  }

  function setImageRenderOption(option: ImageRenderOption | undefined) {
    if (option === undefined) return;
    setImageSize(option.size);
    setImageQuality(option.quality);
  }

  function handleImageAspectRatioChange(nextAspectRatio: string) {
    const option = imageRenderOptions.find((candidate) => candidate.aspect_ratio === nextAspectRatio && candidate.resolution === selectedImageRenderOption?.resolution)
      ?? imageRenderOptions.find((candidate) => candidate.aspect_ratio === nextAspectRatio);
    setImageRenderOption(option);
  }

  function handleImageResolutionChange(nextResolution: string) {
    const option = imageRenderOptions.find((candidate) => candidate.resolution === nextResolution && candidate.aspect_ratio === selectedImageRenderOption?.aspect_ratio)
      ?? imageRenderOptions.find((candidate) => candidate.resolution === nextResolution);
    setImageRenderOption(option);
  }

  useEffect(() => {
    referenceImagesRef.current = referenceImages;
  }, [referenceImages]);

  useEffect(() => {
    if (!workbench || mode !== 'text') {
      setTextHistoryReady(true);
      return;
    }
    setTextHistoryReady(false);
    const restoredConversations = readTextConversations(userID);
    setTextConversations(restoredConversations);
    setActiveTextConversationID(restoredConversations[0]?.id ?? null);
    setTextHistoryReady(true);
  }, [mode, userID, workbench]);

  useEffect(() => {
    if (!workbench || mode !== 'text' || !textHistoryReady) return;
    writeTextConversations(userID, textConversations);
  }, [mode, textConversations, textHistoryReady, userID, workbench]);

  useEffect(() => () => {
    referenceImagesRef.current.forEach(revokeReferencePreview);
  }, []);

  useEffect(() => {
    let active = true;
    void apiClient
      .getResponse<unknown>('/user/models')
      .then(parseUserModelCatalog)
      .then((catalog) => {
        if (!active) return;
        const workbenchModels = catalog.catalog.filter(isWorkbenchModel);
        const requested = searchParams.get('model');
        const next: Record<PlaygroundMode, string> = { text: '', image: '', video: '' };
        (['text', 'image', 'video'] as PlaygroundMode[]).forEach((candidateMode) => {
          const candidates = workbenchModels.filter((item) => modeForModel(item) === candidateMode);
          const requestedModel = candidateMode === 'text' ? requested : null;
          next[candidateMode] = candidates.some((item) => item.model_name === requestedModel)
            ? requestedModel ?? ''
            : candidates[0]?.model_name ?? '';
        });
        setModels(workbenchModels);
        setModelByMode(next);
        setMode(initialMode);
        setCatalogStatus('ready');
        const selected = next.text || next.image || next.video;
        const userID = getAuthSession()?.user.id;
        if (selected !== '' && userID !== undefined) markModelSelected(userID);
      })
      .catch(() => {
        if (active) setCatalogStatus('error');
      });
    return () => {
      active = false;
    };
  }, [initialMode, searchParams]);

  useEffect(() => {
    if (mode === 'image') {
      setImageRenderOption(getImageRenderOptions(selectedOptions)[0]);
      setImageResponseFormat(firstOption(selectedOptions, 'response_formats'));
      setImageCount(Math.max(1, selectedOptions?.min_count ?? 1));
    }
    if (mode === 'video') {
      setVideoSize(firstOption(selectedOptions, 'resolutions'));
      setVideoDuration(selectedOptions?.duration_seconds?.[0] ?? 5);
    }
  }, [mode, selectedCatalog, selectedOptions]);

  const loadBilling = useCallback(async (requestID: string, sequence: number) => {
    if (requestID === '') return;
    try {
      const query = new URLSearchParams({ p: '1', page_size: '1', request_id: requestID });
      const page = parseUserLogPage(await apiClient.get<unknown>(`/log/self?${query.toString()}`));
      if (sequence !== requestSequence.current) return;
      const log = page.items.find((item) => item.request_id === requestID);
      setBilledAmount(log?.billed_amount ?? null);
    } catch {
      // The result remains useful when the asynchronous log is not ready yet.
    }
  }, []);

  useEffect(() => {
    if (mode !== 'video' || videoTask === null || videoSequence === 0 || requestStatus !== 'sending') return;
    if (videoTask.status === 'succeeded' || videoTask.status === 'failed') return;
    let cancelled = false;
    const timer = window.setTimeout(() => {
      void playgroundClient.fetchVideo(videoTask.task_id).then((nextTask) => {
        if (cancelled || videoSequence !== requestSequence.current) return;
        const next = { ...nextTask, request_id: nextTask.request_id || videoTask.request_id };
        setVideoTask(next);
        if (next.status === 'succeeded') {
          setElapsedMs(videoStartedAt === null ? null : Math.max(0, Math.round(performance.now() - videoStartedAt)));
          setRequestStatus('success');
          void loadBilling(next.request_id, videoSequence);
        } else if (next.status === 'failed') {
          setErrorMessage(next.error || '视频生成失败，请稍后重试。');
          setRequestStatus('error');
        }
      }).catch((error: unknown) => {
        if (cancelled || videoSequence !== requestSequence.current) return;
        setErrorMessage(testErrorMessage(error));
        setRequestStatus('error');
      });
    }, VIDEO_POLL_INTERVAL_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [loadBilling, mode, requestStatus, videoSequence, videoStartedAt, videoTask]);

  function clearResults() {
    requestSequence.current += 1;
    setRequestStatus('idle');
    setChatResult(null);
    setImageResult(null);
    setVideoTask(null);
    setVideoSequence(0);
    setVideoStartedAt(null);
    setBilledAmount(null);
    setElapsedMs(null);
    setErrorMessage('');
  }

  function handleModeChange(nextMode: PlaygroundMode) {
    if (nextMode === mode) return;
    clearResults();
    if (mode === 'image' && nextMode !== 'image') {
      setReferenceImages((current) => {
        current.forEach(revokeReferencePreview);
        return [];
      });
      setReferenceError('');
    }
    if (nextMode !== 'text') setActiveTextConversationID(null);
    setMode(nextMode);
    setPromptByMode((current) => ({ ...current, [nextMode]: defaultPromptForMode(nextMode) }));
  }

  function handleModelChange(nextModel: string) {
    clearResults();
    if (mode === 'image') {
      const nextCatalog = models.find((item) => item.model_name === nextModel) ?? null;
      const nextEditInput = imageEditCapability(nextCatalog);
      if (nextEditInput === null || referenceValidationError([], referenceImages.map((reference) => reference.file), nextEditInput) !== '') {
        setReferenceImages((current) => {
          current.forEach(revokeReferencePreview);
          return [];
        });
        setReferenceError(nextEditInput === null ? '当前模型不支持参考图，已清除已选文件。' : '当前模型的参考图限制更严格，已清除不兼容文件。');
      }
    }
    setModelByMode((current) => ({ ...current, [mode]: nextModel }));
    const userID = getAuthSession()?.user.id;
    if (userID !== undefined) markModelSelected(userID);
  }

  function resetImageForm() {
    clearResults();
    setPrompt('');
    setReferenceImages((current) => {
      current.forEach(revokeReferencePreview);
      return [];
    });
    setReferenceError('');
    setImageRenderOption(getImageRenderOptions(selectedOptions)[0]);
    setImageResponseFormat(firstOption(selectedOptions, 'response_formats'));
    setImageCount(Math.max(1, selectedOptions?.min_count ?? 1));
  }

  function addReferenceImages(files: File[]) {
    if (files.length === 0) return;
    if (editInput === null) {
      setReferenceError('当前模型不支持参考图。');
      return;
    }
    const validationError = referenceValidationError(referenceImages, files, editInput);
    if (validationError !== '') {
      setReferenceError(validationError);
      return;
    }
    setReferenceError('');
    setReferenceImages((current) => [...current, ...files.map(createReferencePreview)]);
  }

  function handleReferenceImages(event: ChangeEvent<HTMLInputElement>) {
    const files = Array.from(event.target.files ?? []);
    event.target.value = '';
    addReferenceImages(files);
  }

  function handleReferenceDrop(event: ReactDragEvent<HTMLButtonElement>) {
    event.preventDefault();
    addReferenceImages(Array.from(event.dataTransfer.files));
  }

  function removeReferenceImage(index: number) {
    setReferenceImages((current) => current.filter((reference, referenceIndex) => {
      if (referenceIndex !== index) return true;
      revokeReferencePreview(reference);
      return false;
    }));
  }

  async function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const normalizedPrompt = prompt.trim();
    if (requestStatus === 'sending' || model === '' || normalizedPrompt === '') return;

    const sequence = ++requestSequence.current;
    const startedAt = performance.now();
    setRequestStatus('sending');
    setChatResult(null);
    setImageResult(null);
    setVideoTask(null);
    setVideoSequence(0);
    setVideoStartedAt(null);
    setBilledAmount(null);
    setElapsedMs(null);
    setErrorMessage('');
    try {
      if (mode === 'text') {
        const value = await playgroundClient.chat({ model, prompt: normalizedPrompt });
        if (sequence !== requestSequence.current) return;
        setElapsedMs(Math.max(0, Math.round(performance.now() - startedAt)));
        setChatResult(value);
        if (workbench) {
          const conversationID = activeTextConversationID ?? createTextConversationID();
          setTextConversations((current) => {
            const existing = current.find((conversation) => conversation.id === conversationID);
            const nextConversation: TextConversation = existing === undefined
              ? {
                id: conversationID,
                title: textConversationTitle(normalizedPrompt),
                model,
                updatedAt: Date.now(),
                messages: [
                  { role: 'user', content: normalizedPrompt },
                  { role: 'assistant', content: value.text },
                ],
              }
              : {
                ...existing,
                model,
                updatedAt: Date.now(),
                messages: [
                  ...existing.messages,
                  { role: 'user', content: normalizedPrompt },
                  { role: 'assistant', content: value.text },
                ],
              };
            return [nextConversation, ...current.filter((conversation) => conversation.id !== conversationID)].slice(0, MAX_TEXT_CONVERSATIONS);
          });
          setActiveTextConversationID(conversationID);
          setPrompt('');
          setTextAttachmentName('');
        }
        setRequestStatus('success');
        await loadBilling(value.request_id, sequence);
        return;
      }
      if (mode === 'image') {
        const input = {
          model,
          prompt: normalizedPrompt,
          ...(imageSize === '' ? {} : { size: imageSize }),
          ...(imageQuality === '' ? {} : { quality: imageQuality }),
          n: imageCount,
          ...(referenceImages.length === 0 && imageResponseFormat !== '' ? { response_format: imageResponseFormat } : {}),
        };
        const value = referenceImages.length === 0
          ? await playgroundClient.imageGeneration(input)
          : await playgroundClient.imageEdit(referenceImages.map((reference) => reference.file), input);
        if (sequence !== requestSequence.current) return;
        setElapsedMs(Math.max(0, Math.round(performance.now() - startedAt)));
        setImageResult(value);
        setImageHistory((current) => [value, ...current].slice(0, 6));
        setRequestStatus('success');
        await loadBilling(value.request_id, sequence);
        return;
      }
      const value = await playgroundClient.createVideo({
        model,
        prompt: normalizedPrompt,
        ...(videoSize === '' ? {} : { size: videoSize }),
        duration: videoDuration,
      });
      if (sequence !== requestSequence.current) return;
      setVideoStartedAt(startedAt);
      setVideoTask(value);
      setVideoSequence(sequence);
      if (value.status === 'succeeded') {
        setElapsedMs(Math.max(0, Math.round(performance.now() - startedAt)));
        setRequestStatus('success');
        await loadBilling(value.request_id, sequence);
      } else if (value.status === 'failed') {
        setErrorMessage(value.error || '视频生成失败，请稍后重试。');
        setRequestStatus('error');
      }
    } catch (error) {
      if (sequence !== requestSequence.current) return;
      setElapsedMs(Math.max(0, Math.round(performance.now() - startedAt)));
      setErrorMessage(testErrorMessage(error));
      setRequestStatus('error');
    }
  }

  async function copyExample() {
    setCopyState('idle');
    try {
      await navigator.clipboard.writeText(example);
      setCopyState('success');
    } catch {
      setCopyState('error');
    }
  }

  function handleWorkbenchPointerMove(event: ReactPointerEvent<HTMLDivElement>) {
    if (event.pointerType === 'touch' || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
    const root = event.currentTarget;
    const bounds = root.getBoundingClientRect();
    root.style.setProperty('--zt-pointer-x', `${event.clientX - bounds.left}px`);
    root.style.setProperty('--zt-pointer-y', `${event.clientY - bounds.top}px`);
    root.style.setProperty('--zt-pointer-opacity', '1');
  }

  function handleWorkbenchPointerLeave(event: ReactPointerEvent<HTMLDivElement>) {
    event.currentTarget.style.setProperty('--zt-pointer-opacity', '0');
  }

  async function handleTextAttachment(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (file === undefined) return;
    try {
      const content = await file.text();
      const attachment = `\n\n[附件：${file.name}]\n${content}`;
      setPrompt((current) => `${current}${attachment}`.slice(0, 4_000));
      setTextAttachmentName(file.name);
    } catch {
      setTextAttachmentName('附件读取失败');
    }
  }

  function startTextConversation() {
    clearResults();
    setActiveTextConversationID(null);
    setPrompt('');
    setTextAttachmentName('');
  }

  function openTextConversation(conversationID: string) {
    clearResults();
    setActiveTextConversationID(conversationID);
    setPrompt('');
    setTextAttachmentName('');
  }

  const availableModes = (['text', 'image', 'video'] as PlaygroundMode[]).filter((candidateMode) =>
    models.some((item) => modeForModel(item) === candidateMode),
  );

  function renderBillingNote() {
    return (
      <aside className="playground-billing-note" aria-label={t('计费提醒')} role="note">
        <CircleDollarSign aria-hidden="true" size={18} />
        <div>
          <strong>{t('本次测试会按正常 API 请求扣费')}</strong>
          <p>{t('请求会经过正式模型线路，并在使用日志中留下费用记录。')}</p>
        </div>
      </aside>
    );
  }

  function renderImageReferenceField() {
    if (editInput === null) {
      return (
        <div aria-disabled="true" className="zt-workbench__reference-field zt-workbench__reference-field--disabled">
          <div className="zt-workbench__reference-heading">
            <div>
              <span className="zt-workbench__reference-label">参考图</span>
              <p>当前模型仅支持文本生成图片，请切换到支持图像编辑的模型。</p>
            </div>
            <CircleAlert aria-hidden="true" size={17} />
          </div>
          <div className="zt-workbench__reference-disabled-state">
            <Images aria-hidden="true" size={18} />
            <strong>参考图编辑未开放</strong>
          </div>
        </div>
      );
    }
    const referenceBytes = referenceImages.reduce((total, reference) => total + reference.file.size, 0);
    return (
      <div className="zt-workbench__reference-field">
        <div className="zt-workbench__reference-heading">
          <div>
            <label htmlFor="workbench-image-references">参考图</label>
            <p>最多 {editInput.max_files} 张，单张不超过 {formatBytes(editInput.max_bytes)}，总大小不超过 {formatBytes(editInput.max_total_bytes)}。</p>
            <span className="zt-workbench__reference-summary">已选 {referenceImages.length} / {editInput.max_files} 张 · {formatBytes(referenceBytes)} / {formatBytes(editInput.max_total_bytes)}</span>
          </div>
          <button className="console-button console-button--secondary" type="button" onClick={() => referenceInput.current?.click()}>
            <Plus aria-hidden="true" size={15} />添加参考图
          </button>
        </div>
        <input
          ref={referenceInput}
          id="workbench-image-references"
          aria-label="上传参考图"
          accept={editInput.mime_types.join(',')}
          hidden
          multiple
          type="file"
          onChange={handleReferenceImages}
        />
        {referenceImages.length === 0 ? (
          <button className="zt-workbench__reference-dropzone" type="button" onDragOver={(event) => event.preventDefault()} onDrop={handleReferenceDrop} onClick={() => referenceInput.current?.click()}>
            <Images aria-hidden="true" size={20} />
            <span>拖入参考图，或点击上传</span>
            <small>支持 JPG、PNG、WebP，可一次选择多张</small>
          </button>
        ) : (
          <div className="zt-workbench__reference-list" aria-label="已选参考图">
            {referenceImages.map((reference, index) => (
              <div className="zt-workbench__reference-item" key={`${reference.file.name}-${reference.file.lastModified}-${index}`}>
                {reference.previewUrl === '' ? <Images aria-hidden="true" size={24} /> : <img alt={`参考图 ${index + 1}`} src={reference.previewUrl} />}
                <div><strong>{reference.file.name}</strong><span>{formatBytes(reference.file.size)}</span></div>
                <button aria-label={`删除参考图 ${index + 1}`} className="zt-workbench__reference-remove" type="button" onClick={() => removeReferenceImage(index)}>移除</button>
              </div>
            ))}
            <button className="zt-workbench__reference-add" type="button" onClick={() => referenceInput.current?.click()}>
              <Plus aria-hidden="true" size={16} />继续添加参考图
            </button>
          </div>
        )}
        {referenceError !== '' && <p className="zt-workbench__reference-error" role="alert">{referenceError}</p>}
      </div>
    );
  }

  function renderCatalogState() {
    if (catalogStatus === 'loading') return <div className="console-state">{t('正在加载可测试模型...')}</div>;
    if (catalogStatus === 'error') return <div className="console-alert" role="alert">{t('可测试模型加载失败，请刷新后重试。')}</div>;
    if (models.length === 0) return <div className="console-state">{t('当前账号暂无可在线测试的模型。')}</div>;
    if (modeModels.length === 0) return <div className="console-state">{t('当前能力暂无可用模型。')}</div>;
    return null;
  }

  function renderTextWorkbench() {
    return (
      <div className="zt-workbench zt-workbench--text" onPointerLeave={handleWorkbenchPointerLeave} onPointerMove={handleWorkbenchPointerMove}>
        <div aria-hidden="true" className="zt-workbench__pointer-follow" />
        <aside className="zt-workbench__conversation-rail" aria-label="文本会话">
          <button className="zt-workbench__new-chat" type="button" onClick={startTextConversation}>
            <Plus aria-hidden="true" size={16} />
            <span>新对话</span>
          </button>
          <div className="zt-workbench__conversation-list">
            {textConversations.map((conversation) => (
              <button
                className={conversation.id === activeTextConversationID ? 'zt-workbench__conversation is-active' : 'zt-workbench__conversation'}
                key={conversation.id}
                type="button"
                onClick={() => openTextConversation(conversation.id)}
              >
                <MessageCircle aria-hidden="true" size={16} />
                <span>{conversation.title}</span>
              </button>
            ))}
          </div>
        </aside>
        <main className="zt-workbench__chat">
          <div className="zt-workbench__chat-scroll">
            {activeTextConversation === null ? (
              <div className="zt-workbench__empty">
                <div className="zt-workbench__empty-mark"><Sparkles aria-hidden="true" size={22} /></div>
                <h2>开始一段对话</h2>
                <p>选择模型，输入问题，直接体验当前账号的真实 API 能力。</p>
              </div>
            ) : (
              <div className="zt-workbench__thread">
                {activeTextConversation.messages.map((message, index) => (
                  <div className={`zt-workbench__message zt-workbench__message--${message.role}`} key={`${activeTextConversation.id}-${index}`}>
                    <span>{message.role === 'user' ? '你' : 'ZTAPI'}</span>
                    {message.role === 'user' ? <p>{message.content}</p> : <div className="zt-workbench__answer">{message.content}</div>}
                  </div>
                ))}
              </div>
            )}
          </div>
          <form className="zt-workbench__composer" onSubmit={handleSubmit}>
            <div className="zt-workbench__composer-title">询问 ZTAPI</div>
            <textarea aria-label="输入消息" id="workbench-text-prompt" maxLength={4_000} value={prompt} onChange={(event) => setPrompt(event.target.value)} />
            <div className="zt-workbench__composer-toolbar">
              <div className="zt-workbench__composer-left">
                <button aria-label="添加附件" className="zt-workbench__icon-button" title="添加文本附件" type="button" onClick={() => textAttachmentInput.current?.click()}>
                  <Paperclip aria-hidden="true" size={17} />
                </button>
                <input ref={textAttachmentInput} accept=".txt,.md,.json,.csv" hidden type="file" onChange={handleTextAttachment} />
                <label className="zt-workbench__select-wrap" htmlFor="workbench-text-model">
                  <span className="sr-only">文本模型</span>
                  <select id="workbench-text-model" aria-label="文本模型" value={model} onChange={(event) => handleModelChange(event.target.value)}>
                    {modeModels.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}
                  </select>
                </label>
                {textAttachmentName !== '' && <span className="zt-workbench__attachment" title={textAttachmentName}><FilePlus2 aria-hidden="true" size={14} />{textAttachmentName}</span>}
              </div>
              <button className="zt-workbench__send" disabled={requestStatus === 'sending' || model === '' || prompt.trim() === ''} type="submit">
                {requestStatus === 'sending' ? <RefreshCw aria-hidden="true" className="playground-status__spin" size={16} /> : <Send aria-hidden="true" size={16} />}
                <span>{requestStatus === 'sending' ? '发送中' : '发送'}</span>
              </button>
            </div>
            <p className="zt-workbench__composer-meta">当前使用 Key：已脱敏 · {prompt.length}/4000 字符</p>
          </form>
          {requestStatus === 'error' && <div className="console-alert playground-result-alert" role="alert">{t(errorMessage)}</div>}
        </main>
      </div>
    );
  }

  function renderImageRenderControls(idPrefix: 'workbench' | 'playground') {
    return (
      <>
        <div className="console-field">
          <label htmlFor={`${idPrefix}-image-aspect-ratio`}>图片比例</label>
          <select id={`${idPrefix}-image-aspect-ratio`} value={selectedImageRenderOption?.aspect_ratio ?? ''} onChange={(event) => handleImageAspectRatioChange(event.target.value)}>
            {imageAspectRatios.map((option) => <option key={option} value={option}>{option}</option>)}
          </select>
        </div>
        <div className="console-field">
          <label htmlFor={`${idPrefix}-image-resolution`}>图片分辨率</label>
          <select id={`${idPrefix}-image-resolution`} value={selectedImageRenderOption?.resolution ?? ''} onChange={(event) => handleImageResolutionChange(event.target.value)}>
            {imageResolutions.map((option) => <option key={option} value={option}>{option}</option>)}
          </select>
        </div>
      </>
    );
  }

  function renderImageWorkbench() {
    const gallery = imageHistory.flatMap((result) => result.images.map((image, index) => ({ image, index, requestID: result.request_id })));
    const isEditing = referenceImages.length > 0;
    return (
      <div className="zt-workbench zt-workbench--media" onPointerLeave={handleWorkbenchPointerLeave} onPointerMove={handleWorkbenchPointerMove}>
        <div aria-hidden="true" className="zt-workbench__pointer-follow" />
        <section className="zt-workbench__config" aria-labelledby="image-config-heading">
          <div className="zt-workbench__section-heading"><div><p className="console-eyebrow">{isEditing ? '图像编辑' : '图像生成'}</p><h2 id="image-config-heading">{isEditing ? '编辑一张图片' : '创建一张图片'}</h2></div><Settings2 aria-hidden="true" size={18} /></div>
          {renderBillingNote()}
          {renderCatalogState() ?? <form className="zt-workbench__media-form" onSubmit={handleSubmit}>
            <div className="console-field"><label htmlFor="workbench-image-model">图片模型</label><select id="workbench-image-model" value={model} onChange={(event) => handleModelChange(event.target.value)}>{modeModels.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}</select></div>
            <div className="zt-workbench__model-caption"><span>模型 ID</span><code>{model || '—'}</code></div>
            {renderImageReferenceField()}
            <div className="console-field"><label htmlFor="workbench-image-prompt">图片提示词</label><textarea id="workbench-image-prompt" maxLength={4_000} rows={7} value={prompt} onChange={(event) => setPrompt(event.target.value)} /><p className="console-field__help">{prompt.length} / 4000 字符</p></div>
            <div className="playground-option-grid">{renderImageRenderControls('workbench')}<div className="console-field"><label htmlFor="workbench-image-count">图片数量</label><select id="workbench-image-count" value={imageCount} onChange={(event) => setImageCount(Number(event.target.value))}>{Array.from({ length: Math.max(1, (selectedOptions?.max_count ?? 1) - (selectedOptions?.min_count ?? 1) + 1) }, (_, index) => (selectedOptions?.min_count ?? 1) + index).map((count) => <option key={count} value={count}>{count}</option>)}</select></div>{!isEditing && <div className="console-field"><label htmlFor="workbench-image-format">返回格式</label><select id="workbench-image-format" value={imageResponseFormat} onChange={(event) => setImageResponseFormat(event.target.value)}>{selectedOptions?.response_formats?.map((option) => <option key={option} value={option}>{option}</option>)}</select></div>}</div>
            <div className="zt-workbench__action-row">
              <button aria-label="重置图像参数" className="zt-workbench__reset" title="重置图像参数" type="button" onClick={resetImageForm}><RotateCcw aria-hidden="true" size={17} /></button>
              <button aria-label={isEditing ? '编辑图片' : '生成图片'} className="zt-workbench__run" disabled={requestStatus === 'sending' || model === '' || prompt.trim() === ''} type="submit"><Play aria-hidden="true" size={15} /><span>RUN</span><small>{requestStatus === 'sending' ? '生成中...' : '按实际 API 计费'}</small></button>
            </div>
            <p className="zt-workbench__key-footer"><span>当前使用 Key：已脱敏</span><Link to="/console/keys">更换</Link></p>
          </form>}
          {requestStatus === 'error' && <div className="console-alert playground-result-alert" role="alert">{t(errorMessage)}</div>}
        </section>
        <section className="zt-workbench__results" aria-labelledby="image-results-heading">
          <div className="zt-workbench__section-heading"><div><p className="console-eyebrow">输出记录</p><h2 id="image-results-heading">最近生成</h2></div><span className="zt-workbench__result-count">{gallery.length} 张图片</span></div>
          {gallery.length === 0 ? <div className="zt-workbench__media-empty"><Images aria-hidden="true" size={28} /><strong>暂无生成记录</strong><p>生成完成后，图片会显示在这里。</p></div> : <div className="zt-workbench__gallery">{gallery.map(({ image, index, requestID }) => { const source = imageSource(image); return source === '' ? null : <figure className="zt-workbench__gallery-card" key={`${requestID}-${index}-${source}`}><img alt={`生成结果 ${index + 1}`} src={source} /><figcaption><span>{image.revised_prompt || '生成结果'}</span><a download href={source} rel="noreferrer" target="_blank"><Download aria-hidden="true" size={14} />下载</a></figcaption></figure>; })}</div>}
          {imageResult !== null && <ResultRequest requestID={imageResult.request_id} t={t} />}
        </section>
      </div>
    );
  }

  function renderVideoWorkbench() {
    const hasVideoInput = selectedOptions?.supports_video_input === true;
    return (
      <div className="zt-workbench zt-workbench--media" onPointerLeave={handleWorkbenchPointerLeave} onPointerMove={handleWorkbenchPointerMove}>
        <div aria-hidden="true" className="zt-workbench__pointer-follow" />
        <section className="zt-workbench__config" aria-labelledby="video-config-heading">
          <div className="zt-workbench__section-heading"><div><p className="console-eyebrow">视频生成</p><h2 id="video-config-heading">创建一段视频</h2></div><Settings2 aria-hidden="true" size={18} /></div>
          {renderBillingNote()}
          {renderCatalogState() ?? <form className="zt-workbench__media-form" onSubmit={handleSubmit}>
            <div className="console-field"><label htmlFor="workbench-video-model">视频模型</label><select id="workbench-video-model" value={model} onChange={(event) => handleModelChange(event.target.value)}>{modeModels.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}</select></div>
            <div className="zt-workbench__model-caption"><span>模型 ID</span><code>{model || '—'}</code></div>
            <fieldset className="zt-workbench__mode-field"><legend>生成模式</legend><div className={`zt-workbench__segmented${hasVideoInput ? ' is-multi' : ' is-single'}`}><button aria-pressed="true" className="is-active" type="button">文生视频</button>{hasVideoInput && <><button type="button">关键帧</button><button type="button">视频续写</button></>}</div><p>根据提示词创建视频</p></fieldset>
            <div className="console-field"><label htmlFor="workbench-video-prompt">视频提示词</label><textarea id="workbench-video-prompt" maxLength={4_000} rows={6} value={prompt} onChange={(event) => setPrompt(event.target.value)} /><p className="console-field__help">{prompt.length} / 4000 字符</p></div>
            <div className="zt-workbench__range-field"><div><label htmlFor="workbench-video-duration">视频时长</label><output>{videoDuration} 秒</output></div><input id="workbench-video-duration" max={Math.max(...(selectedOptions?.duration_seconds ?? [20]))} min={Math.min(...(selectedOptions?.duration_seconds ?? [5]))} step="1" type="range" value={videoDuration} onChange={(event) => { const value = Number(event.target.value); const durations = selectedOptions?.duration_seconds ?? [value]; const nearest = durations.reduce((best, current) => Math.abs(current - value) < Math.abs(best - value) ? current : best, durations[0]); setVideoDuration(nearest); }} /><p>可选时长由当前模型目录提供。</p></div>
            <div className="playground-option-grid"><div className="console-field"><label htmlFor="workbench-video-size">分辨率</label><select id="workbench-video-size" value={videoSize} onChange={(event) => setVideoSize(event.target.value)}>{selectedOptions?.resolutions?.map((option) => <option key={option} value={option}>{option}</option>)}</select></div><div className="console-field"><label>能力</label><div className="zt-workbench__capability">{hasVideoInput ? '支持参考视频' : '文生视频'}</div></div></div>
            <button className="zt-workbench__run" disabled={requestStatus === 'sending' || model === '' || prompt.trim() === ''} type="submit"><Play aria-hidden="true" size={16} />{requestStatus === 'sending' ? '处理中...' : '生成视频'}</button>
          </form>}
          {requestStatus === 'error' && <div className="console-alert playground-result-alert" role="alert">{t(errorMessage)}</div>}
        </section>
        <section className="zt-workbench__results" aria-labelledby="video-results-heading">
          <div className="zt-workbench__section-heading"><div><p className="console-eyebrow">任务输出</p><h2 id="video-results-heading">生成结果</h2></div>{videoTask !== null && <span className="zt-workbench__result-count">{videoTask.status}</span>}</div>
          {videoTask === null ? <div className="zt-workbench__media-empty"><VideoIcon aria-hidden="true" size={28} /><strong>暂无生成记录</strong><p>提交任务后，状态和视频会显示在这里。</p></div> : <div className="zt-workbench__video-output">{videoTask.status === 'succeeded' && videoTask.url ? <><div className="playground-status playground-status--succeeded" role="status"><CheckCircle2 aria-hidden="true" size={18} /><strong>视频已生成</strong></div><video aria-label="生成的视频" controls src={videoTask.url} /><a className="console-button console-button--secondary" download href={videoTask.url} rel="noreferrer" target="_blank"><Download aria-hidden="true" size={15} />下载视频</a></> : <div className={`playground-status playground-status--${videoTask.status}`} role="status">{videoTask.status === 'processing' ? <RefreshCw aria-hidden="true" className="playground-status__spin" size={18} /> : videoTask.status === 'failed' ? <CircleAlert aria-hidden="true" size={18} /> : <VideoIcon aria-hidden="true" size={18} />}<div><strong>{videoStatusLabel(videoTask.status)}</strong>{videoTask.progress && <p>{videoTask.progress}</p>}{videoTask.error && <p>{videoTask.error}</p>}</div></div>}<dl className="playground-metrics"><div><dt>{t('任务 ID')}</dt><dd>{videoTask.task_id}</dd></div><div><dt>{t('本次费用')}</dt><dd>{billedAmount === null ? t('入账中') : `${billedAmount.toFixed(6)} U`}</dd></div><div><dt>{t('页面耗时')}</dt><dd>{elapsedMs === null ? '—' : `${elapsedMs} ms`}</dd></div><div><dt>{t('请求状态')}</dt><dd>{videoTask.status}</dd></div></dl><ResultRequest requestID={videoTask.request_id} t={t} /></div>}
        </section>
      </div>
    );
  }

  if (workbench) {
    const title = mode === 'text' ? '文本工作台' : mode === 'image' ? '图像工作台' : '视频工作台';
    return (
      <div aria-label={t(title)} className={`console-page playground-page workbench-page workbench-page--${mode}`}>
        <h1 className="sr-only">{t(title)}</h1>
        {mode === 'text' ? renderTextWorkbench() : mode === 'image' ? renderImageWorkbench() : renderVideoWorkbench()}
        <section className="console-section workbench-code-section" aria-labelledby="playground-code-heading">
          <div className="console-section__heading"><div><p className="console-eyebrow">{t('接入代码')}</p><h2 id="playground-code-heading">{t('把相同模型接入你的程序')}</h2></div><button className="console-icon-action" type="button" onClick={copyExample}><Copy aria-hidden="true" size={15} />{t('复制代码')}</button></div>
          <pre className="playground-code" data-testid="playground-code"><code>{example}</code></pre>
          <p className="playground-copy-status" aria-live="polite" role="status">{copyState === 'success' && t('已复制')}{copyState === 'error' && t('复制失败，请手动复制')}</p>
        </section>
      </div>
    );
  }

  return (
    <div className={`console-page playground-page${workbench ? ' workbench-page' : ''}`}>
      <header className="console-page__header">
        <div>
          <p className="console-eyebrow">{workbench ? t('工作台') : t('接入验证')}</p>
          <h1>{workbench ? t(mode === 'text' ? '文本工作台' : mode === 'image' ? '图像工作台' : '视频工作台') : t('在线 API 测试')}</h1>
        </div>
        <p>{workbench ? t('使用当前账号的 API Key，直接体验真实模型能力。') : t('用当前账号真实调用模型，确认模型、余额、计费和返回结果是否正常。')}</p>
      </header>

      <div className="playground-grid">
        <section className="console-panel" aria-labelledby="playground-request-heading">
          <div className="console-panel__heading">
            <Play aria-hidden="true" size={19} />
            <h2 id="playground-request-heading">{t('发送测试请求')}</h2>
          </div>

          <aside className="playground-billing-note" aria-label={t('计费提醒')} role="note">
            <CircleDollarSign aria-hidden="true" size={18} />
            <div>
              <strong>{t('本次测试会按正常 API 请求扣费')}</strong>
              <p>{t('请求会经过正式模型线路，并在使用日志中留下费用记录。')}</p>
            </div>
          </aside>

          {catalogStatus === 'loading' && <div className="console-state">{t('正在加载可测试模型...')}</div>}
          {catalogStatus === 'error' && <div className="console-alert" role="alert">{t('可测试模型加载失败，请刷新后重试。')}</div>}
          {catalogStatus === 'ready' && models.length === 0 && <div className="console-state">{t('当前账号暂无可在线测试的模型。')}</div>}
          {catalogStatus === 'ready' && models.length > 0 && (
            <>
              {!workbench && <div className="playground-mode-tabs" aria-label={t('工作台能力')}>
                {availableModes.map((candidateMode) => {
                  const label = candidateMode === 'text' ? '文本' : candidateMode === 'image' ? '图片' : '视频';
                  return (
                    <button aria-pressed={candidateMode === mode} className={candidateMode === mode ? 'playground-mode-tab is-active' : 'playground-mode-tab'} key={candidateMode} type="button" onClick={() => handleModeChange(candidateMode)}>
                      {candidateMode === 'image' ? <ImageIcon aria-hidden="true" size={16} /> : candidateMode === 'video' ? <VideoIcon aria-hidden="true" size={16} /> : null}
                      {label}
                    </button>
                  );
                })}
              </div>}
              {modeModels.length === 0 ? <div className="console-state">{t('当前能力暂无可用模型。')}</div> : (
                <form className="playground-form" onSubmit={handleSubmit}>
                  <div className="console-field">
                    <label htmlFor={mode === 'text' ? 'playground-model' : `playground-${mode}-model`}>{mode === 'text' ? t('测试模型') : `${mode === 'image' ? '图片' : '视频'}模型`}</label>
                    <select id={mode === 'text' ? 'playground-model' : `playground-${mode}-model`} value={model} onChange={(event) => handleModelChange(event.target.value)}>
                      {modeModels.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}
                    </select>
                  </div>
                  <div className="console-field">
                    <label htmlFor={mode === 'text' ? 'playground-prompt' : `playground-${mode}-prompt`}>{mode === 'text' ? t('测试问题') : `${mode === 'image' ? '图片' : '视频'}提示词`}</label>
                    <textarea id={mode === 'text' ? 'playground-prompt' : `playground-${mode}-prompt`} maxLength={4_000} rows={mode === 'text' ? 7 : 5} value={prompt} onChange={(event) => setPrompt(event.target.value)} />
                    <p className="console-field__help">{t('{{count}} / 4000 字符', { count: prompt.length })}</p>
                  </div>
                  {mode === 'image' && renderImageReferenceField()}
                  {mode === 'image' && selectedOptions && (
                    <div className="playground-option-grid">
                      {renderImageRenderControls('playground')}
                      <div className="console-field"><label htmlFor="playground-image-count">图片数量</label><select id="playground-image-count" value={imageCount} onChange={(event) => setImageCount(Number(event.target.value))}>{Array.from({ length: Math.max(1, (selectedOptions.max_count ?? 1) - (selectedOptions.min_count ?? 1) + 1) }, (_, index) => (selectedOptions.min_count ?? 1) + index).map((count) => <option key={count} value={count}>{count}</option>)}</select></div>
                      <div className="console-field"><label htmlFor="playground-image-format">图片格式</label><select id="playground-image-format" value={imageResponseFormat} onChange={(event) => setImageResponseFormat(event.target.value)}>{selectedOptions.response_formats?.map((option) => <option key={option} value={option}>{option}</option>)}</select></div>
                    </div>
                  )}
                  {mode === 'video' && selectedOptions && (
                    <div className="playground-option-grid">
                      <div className="console-field"><label htmlFor="playground-video-size">视频分辨率</label><select id="playground-video-size" value={videoSize} onChange={(event) => setVideoSize(event.target.value)}>{selectedOptions.resolutions?.map((option) => <option key={option} value={option}>{option}</option>)}</select></div>
                      <div className="console-field"><label htmlFor="playground-video-duration">视频时长</label><select id="playground-video-duration" value={videoDuration} onChange={(event) => setVideoDuration(Number(event.target.value))}>{selectedOptions.duration_seconds?.map((duration) => <option key={duration} value={duration}>{duration} 秒</option>)}</select></div>
                    </div>
                  )}
                  <button className="console-button console-button--primary" disabled={requestStatus === 'sending' || prompt.trim() === ''} type="submit">
                    <Play aria-hidden="true" size={16} />
                    {requestStatus === 'sending' ? (mode === 'video' ? '正在等待任务...' : t('正在调用...')) : mode === 'image' ? '生成图片' : mode === 'video' ? '生成视频' : t('发送测试请求')}
                  </button>
                </form>
              )}
            </>
          )}
          {requestStatus === 'error' && <div className="console-alert playground-result-alert" role="alert">{t(errorMessage)}</div>}
        </section>

        <section className="console-panel playground-result" aria-labelledby="playground-result-heading">
          <div className="console-panel__heading">
            {requestStatus === 'error' ? <CircleAlert aria-hidden="true" size={19} /> : <CheckCircle2 aria-hidden="true" size={19} />}
            <h2 id="playground-result-heading">{t('调用结果')}</h2>
          </div>
          {mode === 'text' && chatResult !== null ? (
            <div className="playground-result__body">
              <div className="playground-answer">{chatResult.text}</div>
              <dl className="playground-metrics"><div><dt>{t('Token 用量')}</dt><dd>{chatResult.usage?.total_tokens ?? 0} tokens</dd></div><div><dt>{t('本次费用')}</dt><dd>{billedAmount === null ? t('入账中') : `${billedAmount.toFixed(6)} U`}</dd></div><div><dt>{t('页面耗时')}</dt><dd>{elapsedMs === null ? '—' : `${elapsedMs} ms`}</dd></div><div><dt>{t('结束原因')}</dt><dd>{chatResult.finish_reason || '—'}</dd></div></dl>
              <ResultRequest requestID={chatResult.request_id} t={t} />
            </div>
          ) : mode === 'image' && imageResult !== null ? (
            <div className="playground-result__body">
              <div className="playground-media-gallery">{imageResult.images.map((image, index) => { const source = imageSource(image); return source === '' ? null : <figure className="playground-media-card" key={`${source}-${index}`}><img alt={`生成结果 ${index + 1}`} src={source} /><figcaption><span>{image.revised_prompt || '生成结果'}</span><a download href={source} rel="noreferrer" target="_blank"><Download aria-hidden="true" size={14} />下载图片 {index + 1}</a></figcaption></figure>; })}</div>
              <dl className="playground-metrics"><div><dt>{t('图片数量')}</dt><dd>{imageResult.images.length}</dd></div><div><dt>{t('本次费用')}</dt><dd>{billedAmount === null ? t('入账中') : `${billedAmount.toFixed(6)} U`}</dd></div><div><dt>{t('页面耗时')}</dt><dd>{elapsedMs === null ? '—' : `${elapsedMs} ms`}</dd></div><div><dt>{t('请求状态')}</dt><dd>{t('已完成')}</dd></div></dl>
              <ResultRequest requestID={imageResult.request_id} t={t} />
            </div>
          ) : mode === 'video' && videoTask !== null ? (
            <div className="playground-result__body">
              {videoTask.status === 'succeeded' && videoTask.url ? <div className="playground-video-result"><div className="playground-status playground-status--succeeded" role="status"><CheckCircle2 aria-hidden="true" size={18} /><strong>视频已生成</strong></div><video aria-label="生成的视频" controls src={videoTask.url} /><a className="console-button console-button--secondary" download href={videoTask.url} rel="noreferrer" target="_blank"><Download aria-hidden="true" size={15} />下载视频</a></div> : <div className={`playground-status playground-status--${videoTask.status}`} role="status">{videoTask.status === 'processing' ? <RefreshCw aria-hidden="true" className="playground-status__spin" size={18} /> : videoTask.status === 'failed' ? <CircleAlert aria-hidden="true" size={18} /> : <VideoIcon aria-hidden="true" size={18} />}<div><strong>{videoStatusLabel(videoTask.status)}</strong>{videoTask.progress && <p>{videoTask.progress}</p>}{videoTask.error && <p>{videoTask.error}</p>}</div></div>}
              <dl className="playground-metrics"><div><dt>{t('任务 ID')}</dt><dd>{videoTask.task_id}</dd></div><div><dt>{t('本次费用')}</dt><dd>{billedAmount === null ? t('入账中') : `${billedAmount.toFixed(6)} U`}</dd></div><div><dt>{t('页面耗时')}</dt><dd>{elapsedMs === null ? '—' : `${elapsedMs} ms`}</dd></div><div><dt>{t('请求状态')}</dt><dd>{videoTask.status}</dd></div></dl>
              <ResultRequest requestID={videoTask.request_id} t={t} />
            </div>
          ) : <div className="console-state">{t('发送请求后，模型回答和本次费用会显示在这里。')}</div>}
        </section>
      </div>

      <section className="console-section" aria-labelledby="playground-code-heading">
        <div className="console-section__heading"><div><p className="console-eyebrow">{t('接入代码')}</p><h2 id="playground-code-heading">{t('把相同模型接入你的程序')}</h2></div><button className="console-icon-action" type="button" onClick={copyExample}><Copy aria-hidden="true" size={15} />{t('复制代码')}</button></div>
        <pre className="playground-code" data-testid="playground-code"><code>{example}</code></pre>
        <p className="playground-copy-status" aria-live="polite" role="status">{copyState === 'success' && t('已复制')}{copyState === 'error' && t('复制失败，请手动复制')}</p>
      </section>
    </div>
  );
}

function ResultRequest({ requestID, t }: { requestID: string; t: (key: string, vars?: Record<string, string | number>) => string }) {
  return <div className="playground-request-id"><span>{t('请求 ID')}</span><code>{requestID || '—'}</code>{requestID !== '' && <Link className="console-button console-button--secondary" to={`/console/logs?request_id=${encodeURIComponent(requestID)}`}>{t('在使用日志中查看')}</Link>}</div>;
}
