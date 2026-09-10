export function AuthTopology() {
  return (
    <div className="auth-topology" aria-hidden="true">
      <svg viewBox="0 0 560 320" focusable="false">
        <path className="auth-topology__ingress" d="M36 160 H176" />
        <path
          className="auth-topology__route auth-topology__route--blue"
          d="M252 160 C340 160 352 62 488 62"
        />
        <path
          className="auth-topology__route auth-topology__route--cyan"
          d="M252 160 H488"
        />
        <path
          className="auth-topology__route auth-topology__route--lime"
          d="M252 160 C340 160 352 258 488 258"
        />
        <rect
          className="auth-topology__gateway"
          x="176"
          y="118"
          width="76"
          height="84"
          rx="6"
        />
        <circle className="auth-topology__source" cx="36" cy="160" r="8" />
        <circle
          className="auth-topology__node auth-topology__node--blue"
          cx="488"
          cy="62"
          r="8"
        />
        <circle
          className="auth-topology__node auth-topology__node--cyan"
          cx="488"
          cy="160"
          r="8"
        />
        <circle
          className="auth-topology__node auth-topology__node--lime"
          cx="488"
          cy="258"
          r="8"
        />
      </svg>
      <span className="auth-topology__packet auth-topology__packet--one" />
      <span className="auth-topology__packet auth-topology__packet--two" />
    </div>
  );
}
