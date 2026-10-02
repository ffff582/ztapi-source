export function InlineNotice({ message }: { message: string | null }) {
  return message === null ? null : (
    <div className="auth-alert" role="alert">
      {message}
    </div>
  );
}
