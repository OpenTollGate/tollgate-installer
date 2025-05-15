import React from 'react';
import { NDKEvent } from '@nostr-dev-kit/ndk';
import { useNostrReleases } from './NostrReleaseProvider';
import styled from 'styled-components';
import { getReleaseVersion, getReleaseDeviceId, getReleaseOpenWrtVersion } from '../utils/releaseUtils';
import Button from './common/Button';
import PageContainer from './common/PageContainer';

interface WelcomeProps {
  onStart: () => void;
}

const WelcomeContainer = styled.div`
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 100vh;
  padding: ${props => props.theme.space.xl};
  background-color: transparent;
  position: relative;
  z-index: 1;
`;

const ContentCard = styled.div`
  background: #FFFFFF;
  box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
  padding: ${props => props.theme.space.xl};
  border-radius: ${props => props.theme.radii.lg};
  text-align: center;
  max-width: 600px;
  margin: 0 auto;
`;

const Title = styled.h1`
  font-size: ${props => props.theme.fontSizes['3xl']};
  color: ${props => props.theme.colors.text};
  margin-bottom: ${props => props.theme.space.sm};
  text-align: center;
`;

const Description = styled.p`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: ${props => props.theme.space.xl};
  text-align: center;
  max-width: 600px;
`;

const Features = styled.ul`
  margin-bottom: ${props => props.theme.space.xl};
  padding: 0;
  list-style-type: none;
  width: 100%;
  max-width: 450px;
`;

const Feature = styled.li`
  display: flex;
  align-items: flex-start;
  margin-bottom: ${props => props.theme.space.lg};
  font-size: ${props => props.theme.fontSizes.md};
  color: ${props => props.theme.colors.text};
`;

const CheckIcon = styled.span`
  color: ${props => props.theme.colors.primary};
  margin-right: ${props => props.theme.space.md};
  font-size: 1.2em;
  flex-shrink: 0;
`;

const ActionButton = styled(Button)`
  margin-top: ${props => props.theme.space.xl};
  margin-bottom: ${props => props.theme.space.lg};
`;

const VersionsContainer = styled.div`
  margin-top: ${props => props.theme.space.xl};
  border-top: 1px solid ${props => props.theme.colors.border};
  padding-top: ${props => props.theme.space.md};
  width: 100%;
`;

const VersionTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.text};
  margin-bottom: ${props => props.theme.space.md};
  font-weight: ${props => props.theme.fontWeights.semibold};
`;

const VersionsList = styled.ul`
  margin: 0;
  padding: 0;
  list-style-type: none;
  width: 100%;
`;

const VersionItem = styled.li`
  font-size: ${props => props.theme.fontSizes.md};
  color: ${props => props.theme.colors.textSecondary};
  padding: ${props => props.theme.space.sm} 0;
  display: flex;
  align-items: center;
  
  &:before {
    content: '•';
    color: ${props => props.theme.colors.primary};
    margin-right: ${props => props.theme.space.sm};
    font-size: 1.2em;
  }
`;

const LoadingText = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
  font-style: italic;
  padding: ${props => props.theme.space.xs} 0;
`;

const Welcome: React.FC<WelcomeProps> = ({ onStart }) => {
  const { releases, loading, error } = useNostrReleases();
  
  return (
    <PageContainer
      title="TollGate Installer"
      subtitle="Easily install TollGate OS on your fresh router with just a few clicks"
    >
      <Features>
        <Feature>
          <CheckIcon>✓</CheckIcon>
          Automatically detects routers on your network
        </Feature>
        <Feature>
          <CheckIcon>✓</CheckIcon>
          Simple guided installation process
        </Feature>
        <Feature>
          <CheckIcon>✓</CheckIcon>
          Securely transfers and installs TollGate OS
        </Feature>
        <Feature>
          <CheckIcon>✓</CheckIcon>
          Optimized for consecutive router installs
        </Feature>
      </Features>
      
      <ActionButton
        variant="primary"
        size="large"
        fullWidth
        onClick={onStart}
      >
        Start Installation
      </ActionButton>
      
      <VersionsContainer>
        <VersionTitle>Available OS Versions</VersionTitle>
        {error ? (
          <LoadingText>Error loading versions: {error}</LoadingText>
        ) : loading ? (
          <LoadingText>Searching for available OS releases...</LoadingText>
        ) : releases.length === 0 ? (
          <LoadingText>No OS releases found.</LoadingText>
        ) : (
          <VersionsList>
            {releases.map((release: NDKEvent, index: number) => {
              return (
                <VersionItem key={index}>
                  TollGate OS {getReleaseVersion(release)} for {getReleaseDeviceId(release)} (OpenWrt {getReleaseOpenWrtVersion(release)})
                </VersionItem>
              );
            })}
          </VersionsList>
        )}
      </VersionsContainer>
    </PageContainer>
  );
};

export default Welcome;