import {
  forwardRef,
  useState,
  type ComponentPropsWithoutRef,
} from 'react';
import { Eye, EyeOff } from 'lucide-react';

export type PasswordFieldProps = Omit<
  ComponentPropsWithoutRef<'input'>,
  'type'
> & {
  label: string;
  error?: string;
  help?: string;
};

export const PasswordField = forwardRef<HTMLInputElement, PasswordFieldProps>(
  function PasswordField(
    {
      id,
      label,
      error,
      help,
      'aria-describedby': ariaDescribedBy,
      'aria-invalid': ariaInvalid,
      ...inputProps
    },
    ref,
  ) {
    const [visible, setVisible] = useState(false);
    const internalDescriptionId =
      error !== undefined
        ? `${id}-error`
        : help !== undefined
          ? `${id}-help`
          : undefined;
    const describedBy = [ariaDescribedBy, internalDescriptionId]
      .filter(Boolean)
      .join(' ') || undefined;

    return (
      <div className="auth-field">
        <label htmlFor={id}>{label}</label>
        <div className="auth-password">
          <input
            {...inputProps}
            ref={ref}
            id={id}
            type={visible ? 'text' : 'password'}
            aria-describedby={describedBy}
            aria-invalid={error !== undefined ? true : ariaInvalid}
          />
          <button
            type="button"
            aria-label={visible ? '隐藏密码' : '显示密码'}
            title={visible ? '隐藏密码' : '显示密码'}
            onClick={() => setVisible((value) => !value)}
          >
            {visible ? (
              <EyeOff size={18} aria-hidden="true" />
            ) : (
              <Eye size={18} aria-hidden="true" />
            )}
          </button>
        </div>
        {error !== undefined ? (
          <p id={`${id}-error`} className="auth-field__error">
            {error}
          </p>
        ) : help !== undefined ? (
          <p id={`${id}-help`} className="auth-field__help">
            {help}
          </p>
        ) : null}
      </div>
    );
  },
);
