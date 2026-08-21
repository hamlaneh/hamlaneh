/**
 * Scoring behind the four-segment strength meter drawn on the
 * login-force-password-change artboard.
 *
 * The mockup draws the meter but leaves the scale to implementation
 * (docs/design/LOGIN_HANDOFF.md, open question 2), so the rule here is
 * deliberately one a person can predict: only length past the instance
 * minimum and character-class variety, both of which are visible in the field
 * they are typing into.
 */

/**
 * How many of the meter's four segments are filled. 0 is the empty state —
 * nothing typed yet, so nothing is claimed; 1–4 are the four levels.
 */
export type PasswordStrengthScore = 0 | 1 | 2 | 3 | 4;

/**
 * The four character classes the meter can see. They partition the whole code
 * point space, so scripts without case — Persian, Arabic, CJK — land in the
 * fourth class instead of being invisible to the score, exactly as
 * lowercase-only Latin lands in the first.
 */
const CHARACTER_CLASSES = [
  /\p{Ll}/u, // lowercase letters
  /\p{Lu}/u, // uppercase letters
  /\p{Nd}/u, // decimal digits, Persian ۰–۹ included
  /[^\p{Ll}\p{Lu}\p{Nd}]/u, // everything else: symbols, punctuation, spaces, caseless letters
] as const;

function countCharacterClasses(password: string): number {
  return CHARACTER_CLASSES.filter((pattern) => pattern.test(password)).length;
}

/**
 * Length past the instance minimum, as 0–2 points: one for clearing the
 * minimum by a margin rather than by a hair, a second for running
 * substantially longer, where length dominates on its own.
 *
 * Counted in the same units as the minimum-length requirement the user sees
 * ticked off (`password.length`), so the two can never disagree.
 */
function lengthPoints(password: string, minimumLength: number): number {
  const extra = password.length - minimumLength;
  if (extra >= 8) {
    return 2;
  }
  if (extra >= 2) {
    return 1;
  }
  return 0;
}

/**
 * Character-class variety, as 0–2 points: one for mixing at all, a second for
 * using every class the meter distinguishes.
 */
function varietyPoints(password: string): number {
  const classes = countCharacterClasses(password);
  if (classes >= 4) {
    return 2;
  }
  if (classes >= 2) {
    return 1;
  }
  return 0;
}

/**
 * One segment for reaching the minimum plus one per point earned. The two
 * axes can together earn four points; the meter has three segments above the
 * first, so the top level is where it stops.
 */
function levelForPoints(points: number): PasswordStrengthScore {
  if (points <= 0) {
    return 1;
  }
  if (points === 1) {
    return 2;
  }
  if (points === 2) {
    return 3;
  }
  return 4;
}

/**
 * Rates a password for the strength meter.
 *
 * This is a rough guide to how much typing work the password represents, not
 * a verdict on whether it is safe. It cannot tell that a password has been
 * breached, reused elsewhere, or is a well-known phrase, so a high reading is
 * never a promise about security — the copy the meter renders says only how
 * strong the password looks, and never that it is secure.
 *
 * It is advisory in the product too: the instance minimum length is the only
 * rule that gates submission (see `PASSWORD_MIN_LENGTH`), and this score
 * blocks nothing.
 */
export function scorePasswordStrength(
  password: string,
  minimumLength: number,
): PasswordStrengthScore {
  if (password === "") {
    return 0;
  }
  if (password.length < minimumLength) {
    // Below the minimum the password cannot be submitted at all, so no amount
    // of variety should read as progress.
    return 1;
  }
  return levelForPoints(lengthPoints(password, minimumLength) + varietyPoints(password));
}
