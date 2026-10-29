import type { AnchorHTMLAttributes, ComponentPropsWithRef } from "react";
import { Link, type LinkProps } from "react-router";
import "./Button.css";

export type ButtonVariant = "primary" | "secondary" | "destructive" | "brand";

type ButtonProps = ComponentPropsWithRef<"button"> & { variant?: ButtonVariant };
type ButtonLinkProps = AnchorHTMLAttributes<HTMLAnchorElement> & { variant?: ButtonVariant };
type LinkButtonProps = LinkProps & { variant?: ButtonVariant };

/** A button. The brand variant is for the one main first-run action of a screen. */
export function Button({ variant = "secondary", type = "button", className, ...rest }: ButtonProps) {
  return <button type={type} className={buttonClass(variant, className)} {...rest} />;
}

/** A link that looks like a button, for navigation that is not a route of the app. */
export function ButtonLink({ variant = "secondary", className, ...rest }: ButtonLinkProps) {
  return <a className={buttonClass(variant, className)} {...rest} />;
}

/** A link to a route of the app that looks like a button. */
export function LinkButton({ variant = "secondary", className, ...rest }: LinkButtonProps) {
  return <Link className={buttonClass(variant, className as string | undefined)} {...rest} />;
}

function buttonClass(variant: ButtonVariant, extra?: string): string {
  return ["sk-button", `sk-button--${variant}`, extra].filter(Boolean).join(" ");
}
