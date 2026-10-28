import type { AnchorHTMLAttributes, ButtonHTMLAttributes } from "react";
import "./Button.css";

export type ButtonVariant = "primary" | "secondary" | "destructive" | "brand";

type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant };
type ButtonLinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & { variant?: ButtonVariant };

/** A button. The brand variant is for the one main first-run action of a screen. */
export function Button({ variant = "secondary", type = "button", className, ...rest }: ButtonProps) {
  return <button type={type} className={buttonClass(variant, className)} {...rest} />;
}

/** A link that looks like a button, for navigation that is not a route of the app. */
export function ButtonLink({ variant = "secondary", className, ...rest }: ButtonLinkProps) {
  return <a className={buttonClass(variant, className)} {...rest} />;
}

function buttonClass(variant: ButtonVariant, extra?: string): string {
  return ["sk-button", `sk-button--${variant}`, extra].filter(Boolean).join(" ");
}
